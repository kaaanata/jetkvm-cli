package terminal

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
)

// The terminal owner may close its cancelreader only after StreamEvents joins
// the actual Read goroutine. Cancellation alone is not a completion receipt.
func TestTerminalReaderJoinsReadBeforeReturning(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader := &gatedTerminalReader{started: make(chan struct{}), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(reader.release) })
	defer release()
	stream := uv.NewTerminalReader(reader, "xterm")
	done := make(chan error, 1)
	go func() { done <- stream.StreamEvents(ctx, make(chan uv.Event, 1)) }()
	select {
	case <-reader.started:
	case <-time.After(time.Second):
		t.Fatal("terminal read did not start")
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("StreamEvents returned before its Read goroutine exited: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("StreamEvents did not finish after Read exited")
	}
}

type gatedTerminalReader struct{ started, release chan struct{} }

func (r *gatedTerminalReader) Read([]byte) (int, error) {
	close(r.started)
	<-r.release
	return 0, io.EOF
}
