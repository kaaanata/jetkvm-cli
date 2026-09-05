package automation

import (
	"context"
	"errors"
	"image"
	"testing"
	"time"

	"github.com/kaaanata/jetkvm-cli/internal/video"
)

type boundaryDecoder struct{}

func (boundaryDecoder) Name() string { return "boundary" }
func (boundaryDecoder) Reset() error { return nil }
func (boundaryDecoder) Close() error { return nil }
func (boundaryDecoder) Decode(context.Context, video.DecodeRequest) (video.DecodedFrame, error) {
	return video.DecodedFrame{Image: image.NewNRGBA(image.Rect(0, 0, 32, 32))}, nil
}

func TestObservationCacheCannotCrossInputBoundary(t *testing.T) {
	p, _ := video.NewPipeline("device", 1, video.Limits{}, boundaryDecoder{}, nil)
	t.Cleanup(func() { _ = p.Close() })
	stamp := time.Now()
	push := func(seq uint16, at time.Time) {
		_, err := p.Push(t.Context(), video.RTPPacket{Generation: 1, SequenceNumber: seq, Timestamp: uint32(seq), Marker: true, ReceivedAt: at, Payload: []byte{0x78, 0, 2, 0x67, 1, 0, 2, 0x68, 2, 0, 2, 0x65, 3}})
		if err != nil {
			t.Fatal(err)
		}
	}
	push(1, stamp)
	s := &sessionAdapter{video: p, generation: 1}
	// A current decoded frame needs no firmware RPC (protocol intentionally nil).
	first, err := s.Observe(t.Context(), time.Second, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Observe(t.Context(), time.Second, time.Time{})
	if err != nil || again.Observation.ID != first.Observation.ID {
		t.Fatalf("cache miss %v", err)
	}
	boundary := time.Now()
	s.inputCompletedAt = boundary
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if _, err := s.Observe(ctx, time.Second, time.Time{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pre-input frame accepted: %v", err)
	}
	// An explicit observe-after barrier is also enforced, independently of input state.
	s.inputCompletedAt = time.Time{}
	ctx2, cancel2 := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel2()
	if _, err := s.Observe(ctx2, time.Second, boundary); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("explicit barrier ignored: %v", err)
	}
	push(2, time.Now())
	fresh, err := s.Observe(t.Context(), time.Second, boundary)
	if err != nil || fresh.Observation.CapturedAt.Before(boundary) {
		t.Fatalf("fresh observation %v", err)
	}
}
