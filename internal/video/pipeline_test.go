package video

import (
	"context"
	"errors"
	"image"
	"sync/atomic"
	"testing"
	"time"
)

type fakeDecoder struct {
	image  image.Image
	err    error
	closed atomic.Int64
	resets atomic.Int64
}

func (*fakeDecoder) Name() string { return "fake" }
func (d *fakeDecoder) Decode(ctx context.Context, _ DecodeRequest) (DecodedFrame, error) {
	if err := context.Cause(ctx); err != nil {
		return DecodedFrame{}, err
	}
	return DecodedFrame{Image: d.image}, d.err
}
func (d *fakeDecoder) Reset() error { d.resets.Add(1); return nil }
func (d *fakeDecoder) Close() error { d.closed.Add(1); return nil }

func TestEmbeddedDecoderIsNotAdvertised(t *testing.T) {
	factory := EmbeddedDecoder()
	if factory.Available() {
		t.Fatal("unfinished decoder must not be advertised")
	}
	if _, err := factory.New(); !errors.Is(err, ErrDecoderUnavailable) {
		t.Fatalf("New() error = %v, want decoder unavailable", err)
	}
}

type fakePLI struct {
	calls  atomic.Int64
	called chan struct{}
}

func (p *fakePLI) RequestPLI(context.Context, uint64) error {
	p.calls.Add(1)
	if p.called != nil {
		select {
		case p.called <- struct{}{}:
		default:
		}
	}
	return nil
}

func TestPipelinePublishesFreshGenerationBoundObservation(t *testing.T) {
	now := time.Date(2026, 9, 5, 1, 2, 3, 0, time.UTC)
	decoder := &fakeDecoder{image: image.NewNRGBA(image.Rect(0, 0, 1920, 1080))}
	pipeline, err := NewPipeline("device-1", 4, Limits{}, decoder, nil)
	if err != nil {
		t.Fatal(err)
	}
	pipeline.now = func() time.Time { return now }
	pipeline.newID = func() (string, error) { return "obs-test", nil }
	defer pipeline.Close()

	stap := []byte{0x78, 0, 2, 0x67, 1, 0, 2, 0x68, 2, 0, 2, 0x65, 3}
	observation, err := pipeline.Push(t.Context(), packet(4, 8, 77, true, now.Add(-time.Millisecond), stap))
	if err != nil {
		t.Fatal(err)
	}
	if observation.ID != "obs-test" || observation.Trust != ObservationTrust || observation.Frame.Generation != 4 || observation.Frame.Width != 1920 {
		t.Fatalf("observation = %+v", observation)
	}
	got, err := pipeline.AwaitObservation(t.Context(), ObserveRequest{Generation: 4, Freshness: time.Second})
	if err != nil || got.ID != observation.ID {
		t.Fatalf("AwaitObservation() = (%+v, %v)", got, err)
	}
	if _, err := pipeline.AwaitObservation(t.Context(), ObserveRequest{Generation: 3}); !errors.Is(err, ErrGenerationMismatch) {
		t.Fatalf("generation error = %v", err)
	}
}

func TestPipelineRequestsPLIAndHonorsCancellation(t *testing.T) {
	requester := &fakePLI{called: make(chan struct{}, 1)}
	pipeline, err := NewPipeline("device-1", 1, Limits{MinPLIInterval: time.Hour}, &fakeDecoder{image: image.NewNRGBA(image.Rect(0, 0, 1, 1))}, requester)
	if err != nil {
		t.Fatal(err)
	}
	defer pipeline.Close()

	ctx, cancel := context.WithCancelCause(t.Context())
	want := errors.New("stop observation")
	result := make(chan error, 1)
	go func() {
		_, err := pipeline.AwaitObservation(ctx, ObserveRequest{Generation: 1})
		result <- err
	}()
	select {
	case <-requester.called:
	case <-time.After(time.Second):
		t.Fatal("AwaitObservation did not request a keyframe")
	}
	cancel(want)
	if err := <-result; !errors.Is(err, want) {
		t.Fatalf("AwaitObservation() error = %v, want cancellation cause", err)
	}
	if requester.calls.Load() != 1 {
		t.Fatalf("PLI calls = %d, want 1", requester.calls.Load())
	}
}

func TestPipelineRejectsOversizedDecodedFrame(t *testing.T) {
	pipeline, err := NewPipeline("device-1", 1, Limits{MaxWidth: 100, MaxHeight: 100, MaxPixels: 10_000}, &fakeDecoder{image: image.NewNRGBA(image.Rect(0, 0, 101, 100))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pipeline.Close()
	stap := []byte{0x78, 0, 2, 0x67, 1, 0, 2, 0x68, 2, 0, 2, 0x65, 3}
	if _, err := pipeline.Push(t.Context(), packet(1, 1, 1, true, time.Now(), stap)); !errors.Is(err, ErrDimensionsExceeded) {
		t.Fatalf("Push() error = %v, want dimensions exceeded", err)
	}
}

func TestPipelineResetFencesOldGeneration(t *testing.T) {
	decoder := &fakeDecoder{image: image.NewNRGBA(image.Rect(0, 0, 1, 1))}
	pipeline, err := NewPipeline("device-1", 1, Limits{}, decoder, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pipeline.Close()
	if err := pipeline.Reset(2); err != nil {
		t.Fatal(err)
	}
	if decoder.resets.Load() != 1 {
		t.Fatalf("decoder reset count = %d, want 1", decoder.resets.Load())
	}
	if _, err := pipeline.Push(t.Context(), packet(1, 1, 1, true, time.Now(), []byte{0x65, 1})); !errors.Is(err, ErrGenerationMismatch) {
		t.Fatalf("old packet error = %v, want generation mismatch", err)
	}
}

func TestPipelineClassifiesDeadlineWithoutFrame(t *testing.T) {
	pipeline, err := NewPipeline("device-1", 1, Limits{}, &fakeDecoder{image: image.NewNRGBA(image.Rect(0, 0, 1, 1))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pipeline.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	_, err = pipeline.AwaitObservation(ctx, ObserveRequest{Generation: 1})
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrVideoUnavailable) {
		t.Fatalf("AwaitObservation() error = %v", err)
	}
}

func TestPipelineCloseWakesWaiter(t *testing.T) {
	decoder := &fakeDecoder{image: image.NewNRGBA(image.Rect(0, 0, 1, 1))}
	pipeline, err := NewPipeline("device-1", 1, Limits{}, decoder, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := pipeline.AwaitObservation(t.Context(), ObserveRequest{Generation: 1})
		result <- err
	}()
	if err := pipeline.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrPipelineClosed) {
			t.Fatalf("waiter error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter did not wake")
	}
	if decoder.closed.Load() != 1 {
		t.Fatalf("decoder close count = %d", decoder.closed.Load())
	}
}
