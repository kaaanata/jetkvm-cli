package control

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestExecuteCancellationJoinsTerminalCallback(t *testing.T) {
	registry, _, _, _ := newTestRegistry(t)
	handle, err := registry.Open(t.Context(), OpenRequest{DeviceID: "device-a", Capabilities: []string{"input"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, finish := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- registry.Execute(ctx, "device-a", Ref{ID: handle.ID, ExpectedGeneration: handle.Generation}, "input", func(ctx context.Context, _ Session) error {
			close(entered)
			<-ctx.Done()
			<-finish
			return context.Cause(ctx)
		})
	}()
	<-entered
	cancel()
	select {
	case <-result:
		close(finish)
		t.Fatal("Execute returned before cleanup and receipt fields were terminal")
	case <-time.After(20 * time.Millisecond):
	}
	close(finish)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel result = %v", err)
	}
}
