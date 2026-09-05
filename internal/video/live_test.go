package video

import (
	"context"
	"errors"
	"image"
	"testing"
	"time"
)

type controlledLiveDecoder struct {
	started chan DecodeRequest
	release chan struct{}
}

func (*controlledLiveDecoder) Name() string { return "controlled" }
func (*controlledLiveDecoder) Reset() error { return nil }
func (*controlledLiveDecoder) Close() error { return nil }
func (d *controlledLiveDecoder) Decode(ctx context.Context, r DecodeRequest) (DecodedFrame, error) {
	d.started <- r
	select {
	case <-ctx.Done():
		return DecodedFrame{}, ctx.Err()
	case <-d.release:
	}
	return DecodedFrame{Image: image.NewNRGBA(image.Rect(0, 0, 32, 32))}, nil
}
func liveTestPacket(generation uint64, seq uint16, stamp uint32) RTPPacket {
	return packet(generation, seq, stamp, true, time.Now(), []byte{0x78, 0, 2, 0x67, 1, 0, 2, 0x68, 2, 0, 2, 0x65, 3})
}
func liveStarted(t *testing.T, d *controlledLiveDecoder) DecodeRequest {
	t.Helper()
	select {
	case r := <-d.started:
		return r
	case <-time.After(time.Second):
		t.Fatal("decoder did not start")
		return DecodeRequest{}
	}
}
func TestLiveKeepsOnlyNewestCompleteIDR(t *testing.T) {
	d := &controlledLiveDecoder{started: make(chan DecodeRequest, 8), release: make(chan struct{})}
	pli := &fakePLI{}
	p, err := NewPipeline("device-1", 1, Limits{}, d, pli)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	if err := p.StartLive(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Push(t.Context(), liveTestPacket(1, 1, 1)); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal(err)
	}
	if err := p.PushLive(t.Context(), liveTestPacket(1, 1, 1)); err != nil {
		t.Fatal(err)
	}
	if r := liveStarted(t, d); r.AccessUnit.RTPTime != 1 {
		t.Fatal(r)
	}
	// A decoder blocked indefinitely must not stop the RTP reader.
	for _, stamp := range []uint32{2, 3, 4} {
		done := make(chan error, 1)
		go func() { done <- p.PushLive(t.Context(), liveTestPacket(1, uint16(stamp), stamp)) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("ingestion waited for decoder")
		}
	}
	// An incomplete newer AU cannot replace the complete pending IDR.
	if err := p.PushLive(t.Context(), packet(1, 5, 5, false, time.Now(), []byte{0x7c, 0x85, 1})); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	var pending *AccessUnit
	if len(p.pending) > 0 {
		pending = p.pending[0]
	}
	p.mu.Unlock()
	if pending == nil || pending.RTPTime != 4 {
		t.Fatalf("pending=%+v", pending)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if _, err := p.AwaitObservation(ctx, ObserveRequest{Generation: 1}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if pli.calls.Load() > 1 {
		t.Fatal("PLI flooded while a complete IDR was already decoding")
	}
	d.release <- struct{}{}
	if r := liveStarted(t, d); r.AccessUnit.RTPTime != 4 {
		t.Fatalf("decoded queued old IDR: %d", r.AccessUnit.RTPTime)
	}
	d.release <- struct{}{}
	ctx2, cancel2 := context.WithTimeout(t.Context(), time.Second)
	defer cancel2()
	obs, err := p.AwaitObservation(ctx2, ObserveRequest{Generation: 1, NotBefore: pending.ReceivedAt})
	if err != nil {
		t.Fatal(err)
	}
	if obs.Frame.RTPTime != 4 || !obs.CapturedAt.Equal(pending.ReceivedAt) {
		t.Fatal("latest source receipt not preserved", obs.Frame)
	}
}

func TestLiveResetAndCloseJoinWorker(t *testing.T) {
	d := &controlledLiveDecoder{started: make(chan DecodeRequest, 8), release: make(chan struct{})}
	p, err := NewPipeline("device-1", 1, Limits{}, d, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := p.StartLive(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.PushLive(ctx, liveTestPacket(1, 1, 1)); err != nil {
		t.Fatal(err)
	}
	liveStarted(t, d)
	if err := p.PushLive(ctx, liveTestPacket(1, 2, 2)); err != nil {
		t.Fatal(err)
	}
	reset := make(chan error, 1)
	go func() { reset <- p.Reset(2) }()
	select {
	case err := <-reset:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Reset did not cancel live decoder")
	}
	p.mu.Lock()
	pending, latest := p.pending, p.latest
	p.mu.Unlock()
	if pending != nil || latest != nil {
		t.Fatal("Reset retained old generation")
	}
	if err := p.PushLive(ctx, liveTestPacket(1, 3, 3)); !errors.Is(err, ErrGenerationMismatch) {
		t.Fatal(err)
	}
	if err := p.PushLive(ctx, liveTestPacket(2, 1, 1)); err != nil {
		t.Fatal(err)
	}
	if r := liveStarted(t, d); r.AccessUnit.Generation != 2 {
		t.Fatal(r)
	}
	closed := make(chan error, 1)
	go func() { closed <- p.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not join live worker")
	}
	select {
	case <-p.liveDone:
	default:
		t.Fatal("worker outlived Close")
	}
	if err := p.PushLive(ctx, liveTestPacket(2, 2, 2)); !errors.Is(err, ErrPipelineClosed) {
		t.Fatal(err)
	}
}

func TestLiveDecodeFailureIsSourceFenced(t *testing.T) {
	d := &fakeDecoder{err: ErrDecodeFailed}
	p, err := NewPipeline("device-1", 1, Limits{}, d, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	if err := p.StartLive(t.Context()); err != nil {
		t.Fatal(err)
	}
	input := liveTestPacket(1, 1, 1)
	if err := p.PushLive(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := p.AwaitObservation(ctx, ObserveRequest{Generation: 1, NotBefore: input.ReceivedAt}); !errors.Is(err, ErrDecodeFailed) {
		t.Fatalf("decode failure hidden: %v", err)
	}
	// A future capture request must not inherit an older AU's decode error.
	ctx2, cancel2 := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel2()
	if _, err := p.AwaitObservation(ctx2, ObserveRequest{Generation: 1, NotBefore: input.ReceivedAt.Add(time.Second)}); !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrDecodeFailed) {
		t.Fatalf("old decode error crossed source boundary: %v", err)
	}
}
