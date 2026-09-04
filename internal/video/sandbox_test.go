package video

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"
)

func TestSandboxExecutionCancellationAndJoin(t *testing.T) {
	// (module (func (export "_start") (loop br 0))) deliberately never exits.
	spin := []byte{0, 97, 115, 109, 1, 0, 0, 0, 1, 4, 1, 96, 0, 0, 3, 2, 1, 0, 7, 10, 1, 6, '_', 's', 't', 'a', 'r', 't', 0, 0, 10, 9, 1, 7, 0, 3, 64, 12, 0, 11, 11}
	for _, stop := range []string{"deadline", "close", "reset"} {
		t.Run(stop, func(t *testing.T) {
			d := &embeddedDecoder{gate: make(chan struct{}, 1)}
			d.runtime = wazero.NewRuntimeWithConfig(t.Context(), decoderRuntimeConfig())
			var err error
			d.compiled, err = d.runtime.CompileModule(t.Context(), spin)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = d.Close() })
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			result := make(chan error, 1)
			req := fixtureRequest(t, "red-high")
			go func() { _, err := d.Decode(ctx, req); result <- err }()
			if stop != "deadline" {
				for {
					d.mu.Lock()
					active := d.cancel != nil
					d.mu.Unlock()
					if active {
						break
					}
					if ctx.Err() != nil {
						t.Fatal("execution did not start")
					}
					time.Sleep(time.Millisecond)
				}
				if stop == "close" {
					err = d.Close()
				} else {
					err = d.Reset()
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("WASM execution was not joined")
			}
		})
	}
}

func TestSandboxMemoryAndOutputLimits(t *testing.T) {
	r := wazero.NewRuntimeWithConfig(t.Context(), decoderRuntimeConfig())
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	// A memory minimum of 8193 pages exceeds the production 8192-page cap.
	tooLarge := []byte{0, 97, 115, 109, 1, 0, 0, 0, 5, 4, 1, 0, 0x81, 0x40}
	if _, err := r.CompileModule(t.Context(), tooLarge); err == nil {
		t.Fatal("accepted oversized memory")
	}
	// Export a small memory with no declared max. The runtime must impose ours.
	small := []byte{0, 97, 115, 109, 1, 0, 0, 0, 5, 3, 1, 0, 1, 7, 10, 1, 6, 'm', 'e', 'm', 'o', 'r', 'y', 2, 0}
	m, err := r.Instantiate(t.Context(), small)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Memory().Grow(decoderMemoryPages); ok {
		t.Fatal("memory grew beyond cap")
	}
	if m.Memory().Size() != 65536 {
		t.Fatal("failed growth changed memory")
	}
	w := boundedOutput{max: 8}
	if _, err := w.Write(make([]byte, 9)); !errors.Is(err, io.ErrShortWrite) || w.buf.Len() != 0 {
		t.Fatal("output bound failed", err)
	}
}
