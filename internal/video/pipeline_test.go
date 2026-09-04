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

func TestEmbeddedDecoderAvailable(t *testing.T) {
	factory := EmbeddedDecoder()
	if !factory.Available() {
		t.Fatal("embedded decoder must be available")
	}
	d, err := factory.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
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

func TestAwaitNotBeforeRetriesPLIWithoutNotification(t *testing.T) {
	requester := &fakePLI{called: make(chan struct{}, 8)}
	p, err := NewPipeline("device-1", 1, Limits{MinPLIInterval: 10 * time.Millisecond}, &fakeDecoder{}, requester)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	boundary := time.Now()
	p.latest = &Observation{ID: "cached", CapturedAt: boundary, Frame: FrameMetadata{ReceivedAt: boundary.Add(-time.Second)}}
	p.lastPLI = boundary // First attempt is rate limited, with no RTP to wake it.
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result := make(chan *Observation, 1)
	failure := make(chan error, 1)
	go func() {
		obs, err := p.AwaitObservation(ctx, ObserveRequest{Generation: 1, NotBefore: boundary})
		if err != nil {
			failure <- err
		} else {
			result <- obs
		}
	}()
	for range 2 {
		select {
		case <-requester.called:
		case obs := <-result:
			t.Fatalf("returned pre-boundary frame: %+v", obs)
		case err := <-failure:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal("rate-limited PLI was not retried")
		}
	}
	p.mu.Lock()
	p.latest = &Observation{ID: "new", CapturedAt: time.Now(), Frame: FrameMetadata{ReceivedAt: boundary}}
	close(p.notify)
	p.notify = make(chan struct{})
	p.mu.Unlock()
	select {
	case obs := <-result:
		if obs.ID != "new" {
			t.Fatal(obs)
		}
	case err := <-failure:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal("new frame not delivered")
	}
}

func TestAwaitFreshnessUsesSourceReceiveTime(t *testing.T) {
	p, err := NewPipeline("device-1", 1, Limits{}, &fakeDecoder{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	now := time.Now()
	p.latest = &Observation{CapturedAt: now, Frame: FrameMetadata{ReceivedAt: now.Add(-defaultFreshness - time.Second), DecodedAt: now}}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if _, err := p.AwaitObservation(ctx, ObserveRequest{Generation: 1}); !errors.Is(err, ErrFrameStale) {
		t.Fatalf("stale source accepted: %v", err)
	}
	if _, err := p.AwaitObservation(t.Context(), ObserveRequest{Generation: 1, Freshness: defaultFreshness + 2*time.Second}); err != nil {
		t.Fatal(err)
	}
	// The default must not widen an explicitly stricter freshness requirement.
	p.latest.Frame.ReceivedAt = time.Now().Add(-2 * time.Second)
	ctx2, cancel2 := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel2()
	if _, err := p.AwaitObservation(ctx2, ObserveRequest{Generation: 1, Freshness: time.Second}); !errors.Is(err, ErrFrameStale) {
		t.Fatalf("explicit strict freshness was widened: %v", err)
	}
}

func TestPipelineMatchesGeometry(t *testing.T) {
	d := &fakeDecoder{image: image.NewNRGBA(image.Rect(0, 0, 32, 32))}
	p, err := NewPipeline("device-1", 1, Limits{}, d, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	if p.MatchesGeometry(1, 32, 32) {
		t.Fatal("matched before decode")
	}
	stap := []byte{0x78, 0, 2, 0x67, 1, 0, 2, 0x68, 2, 0, 2, 0x65, 3}
	if _, err := p.Push(t.Context(), packet(1, 1, 1, true, time.Now(), stap)); err != nil {
		t.Fatal(err)
	}
	if !p.MatchesGeometry(1, 32, 32) || p.MatchesGeometry(2, 32, 32) {
		t.Fatal("generation/geometry mismatch")
	}
	d.image = image.NewNRGBA(image.Rect(0, 0, 64, 32))
	if _, err := p.Push(t.Context(), packet(1, 2, 2, true, time.Now(), stap)); err != nil {
		t.Fatal(err)
	}
	if p.MatchesGeometry(1, 32, 32) || !p.MatchesGeometry(1, 64, 32) {
		t.Fatal("old geometry remained valid")
	}
	if err := p.Reset(2); err != nil {
		t.Fatal(err)
	}
	if p.MatchesGeometry(1, 64, 32) || p.MatchesGeometry(2, 64, 32) {
		t.Fatal("reset retained geometry")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if p.MatchesGeometry(2, 64, 32) {
		t.Fatal("closed pipeline matched")
	}
}
