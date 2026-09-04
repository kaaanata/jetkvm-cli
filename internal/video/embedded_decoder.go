package video

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

//go:embed decoder.wasm
var decoderWASM []byte

// Retain attribution in the shipped single-file binary as well as source.
//
//go:embed DECODER_LICENSES.txt
var decoderLicenses string

const decoderName = "hi264-v0.10.0-wasi"
const decoderMemoryPages = 8192 // 512 MiB hard linear-memory limit per instance.
const decoderTimeout = 10 * time.Second

func decoderRuntimeConfig() wazero.RuntimeConfig {
	return wazero.NewRuntimeConfig().WithMemoryLimitPages(decoderMemoryPages).WithCloseOnContextDone(true)
}

type embeddedDecoderFactory struct{}

func (embeddedDecoderFactory) Name() string { return decoderName }
func (embeddedDecoderFactory) Available() bool {
	return len(decoderWASM) != 0 && len(decoderLicenses) != 0
}
func (embeddedDecoderFactory) New() (Decoder, error) {
	return &embeddedDecoder{gate: make(chan struct{}, 1)}, nil
}

type embeddedDecoder struct {
	gate     chan struct{}
	mu       sync.Mutex
	closed   bool
	cancel   context.CancelFunc
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
}

func (*embeddedDecoder) Name() string { return decoderName }

func (d *embeddedDecoder) Decode(parent context.Context, req DecodeRequest) (DecodedFrame, error) {
	// Compilation is work on a fixed, trusted artifact and remains owned by the
	// caller context. The hostile-input execution budget starts only afterwards.
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	select {
	case d.gate <- struct{}{}:
		defer func() { <-d.gate }()
	case <-ctx.Done():
		return DecodedFrame{}, ctx.Err()
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return DecodedFrame{}, ErrPipelineClosed
	}
	d.cancel = cancel
	d.mu.Unlock()
	defer func() { d.mu.Lock(); d.cancel = nil; d.mu.Unlock() }()
	if err := ctx.Err(); err != nil {
		return DecodedFrame{}, err
	}
	w, h, err := validateIDR(req)
	if err != nil {
		return DecodedFrame{}, err
	}
	if d.runtime == nil {
		d.runtime = wazero.NewRuntimeWithConfig(ctx, decoderRuntimeConfig())
		if _, err := wasi_snapshot_preview1.Instantiate(ctx, d.runtime); err != nil {
			d.closeRuntime()
			return DecodedFrame{}, fmt.Errorf("%w: WASI: %v", ErrDecodeFailed, err)
		}
		compiled, err := d.runtime.CompileModule(ctx, decoderWASM)
		if err != nil {
			d.closeRuntime()
			if ctx.Err() != nil {
				return DecodedFrame{}, ctx.Err()
			}
			return DecodedFrame{}, fmt.Errorf("%w: compile: %v", ErrDecodeFailed, err)
		}
		d.compiled = compiled
	}
	if err := ctx.Err(); err != nil {
		return DecodedFrame{}, err
	}
	ctx, executionCancel := context.WithTimeout(ctx, decoderTimeout)
	defer executionCancel()
	// Allocate only after the independently parsed SPS has passed all bounds.
	out := boundedOutput{max: 8 + 4*w*h}
	stderr := boundedOutput{max: 4096}
	mod, err := d.runtime.InstantiateModule(ctx, d.compiled, wazero.NewModuleConfig().
		WithName("").WithStdin(bytes.NewReader(req.AccessUnit.AnnexB)).WithStdout(&out).WithStderr(&stderr))
	if mod != nil {
		_ = mod.Close(context.Background())
	}
	if ctx.Err() != nil {
		return DecodedFrame{}, ctx.Err()
	}
	if exit, ok := errors.AsType[*sys.ExitError](err); ok && exit.ExitCode() == 0 {
		err = nil
	}
	if err != nil {
		return DecodedFrame{}, fmt.Errorf("%w: WASI execution: %v", ErrDecodeFailed, err)
	}
	data := out.buf.Bytes()
	if len(data) != out.max || int(binary.LittleEndian.Uint32(data[:4])) != w || int(binary.LittleEndian.Uint32(data[4:8])) != h {
		return DecodedFrame{}, fmt.Errorf("%w: invalid RGBA receipt", ErrDecodeFailed)
	}
	return DecodedFrame{Image: &image.NRGBA{Pix: data[8:], Stride: 4 * w, Rect: image.Rect(0, 0, w, h)}}, nil
}

// Reset/Close cancel and join the current execution; there is no detached decoder
// worker. Each Decode already uses a fresh module with no reference-picture state.
func (d *embeddedDecoder) Reset() error { return d.stop(false) }
func (d *embeddedDecoder) Close() error { return d.stop(true) }
func (d *embeddedDecoder) stop(close bool) error {
	d.mu.Lock()
	if close {
		d.closed = true
	}
	if d.cancel != nil {
		d.cancel()
	}
	d.mu.Unlock()
	d.gate <- struct{}{}
	defer func() { <-d.gate }()
	d.closeRuntime()
	return nil
}
func (d *embeddedDecoder) closeRuntime() {
	if d.runtime != nil {
		_ = d.runtime.Close(context.Background())
	}
	d.runtime, d.compiled = nil, nil
}

type boundedOutput struct {
	buf bytes.Buffer
	max int
}

func (w *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > w.max-w.buf.Len() {
		return 0, io.ErrShortWrite
	}
	return w.buf.Write(p)
}
