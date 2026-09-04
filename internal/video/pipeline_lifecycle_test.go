package video

import (
	"context"
	"errors"
	"testing"
	"time"
)

type waitingDecoder struct {
	entered chan struct{}
	exited  chan struct{}
}

func (*waitingDecoder) Name() string { return "waiting" }
func (d *waitingDecoder) Decode(ctx context.Context, _ DecodeRequest) (DecodedFrame, error) {
	close(d.entered)
	<-ctx.Done()
	close(d.exited)
	return DecodedFrame{}, ctx.Err()
}
func (*waitingDecoder) Reset() error { return nil }
func (*waitingDecoder) Close() error { return nil }

func TestPipelineAwaitAndLifecycleDuringDecode(t *testing.T) {
	for _, stop := range []string{"close", "reset"} {
		t.Run(stop, func(t *testing.T) {
			d := &waitingDecoder{entered: make(chan struct{}), exited: make(chan struct{})}
			p, err := NewPipeline("device-1", 1, Limits{}, d, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.Close() })
			pushDone := make(chan error, 1)
			go func() {
				_, err := p.Push(t.Context(), packet(1, 1, 1, true, time.Now(), []byte{0x78, 0, 2, 0x67, 1, 0, 2, 0x68, 2, 0, 2, 0x65, 3}))
				pushDone <- err
			}()
			select {
			case <-d.entered:
			case <-time.After(time.Second):
				t.Fatal("decode not entered")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			awaitDone := make(chan error, 1)
			go func() { _, err := p.AwaitObservation(ctx, ObserveRequest{Generation: 1}); awaitDone <- err }()
			select {
			case err := <-awaitDone:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("Await blocked behind Decode")
			}
			lifecycleDone := make(chan error, 1)
			go func() {
				if stop == "close" {
					lifecycleDone <- p.Close()
				} else {
					lifecycleDone <- p.Reset(2)
				}
			}()
			select {
			case err := <-lifecycleDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("lifecycle did not cancel and join Decode")
			}
			select {
			case <-d.exited:
			default:
				t.Fatal("decoder still running after lifecycle returned")
			}
			select {
			case err := <-pushDone:
				if err == nil {
					t.Fatal("canceled frame published")
				}
			case <-time.After(time.Second):
				t.Fatal("Push did not join")
			}
		})
	}
}
