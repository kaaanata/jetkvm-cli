package video

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/tetratelabs/wazero/api"
	"image"
	"io"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed decoder.wasm
var decoderWASM []byte

// Retain attribution in the shipped single-file binary as well as source.
//
//go:embed DECODER_LICENSES.txt
var decoderLicenses string

const decoderName = "ffmpeg-9.0.1-h264-wasi"
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
	drained       bool
	gate          chan struct{}
	mu            sync.Mutex
	closed        bool
	cancel        context.CancelFunc
	runtime       wazero.Runtime
	compiled      wazero.CompiledModule
	module        api.Module
	serial        uint64
	sources       map[uint64]AccessUnit
	width, height int
	color         streamColor
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
	if d.drained && len(req.AccessUnit.AnnexB) > 0 {
		d.dropModule()
	}
	if err := d.validate(req); err != nil {
		return DecodedFrame{}, err
	}
	var err error

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
	if d.module == nil || d.module.IsClosed() {
		d.module, err = d.runtime.InstantiateModule(ctx, d.compiled, wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize", "_start"))
		if err != nil {
			d.dropModule()
			if ctx.Err() != nil {
				return DecodedFrame{}, ctx.Err()
			}
			return DecodedFrame{}, fmt.Errorf("%w: instantiate: %v", ErrDecodeFailed, err)
		}
	}
	fail := func(err error) (DecodedFrame, error) {
		d.dropModule()
		if ctx.Err() != nil {
			return DecodedFrame{}, ctx.Err()
		}
		return DecodedFrame{}, fmt.Errorf("%w: %w", ErrDecodeFailed, err)
	}
	call := func(name string, args ...uint64) (uint64, error) {
		f := d.module.ExportedFunction(name)
		if f == nil {
			return 0, fmt.Errorf("missing decoder export %s", name)
		}
		r, e := f.Call(ctx, args...)
		if e != nil {
			return 0, e
		}
		return r[0], nil
	}
	ptr, err := call("input_ptr")
	if err != nil {
		return fail(err)
	}
	if !d.module.Memory().Write(uint32(ptr), req.AccessUnit.AnnexB) {
		return fail(errors.New("input bounds"))
	}
	d.serial++
	if d.sources == nil {
		d.sources = make(map[uint64]AccessUnit)
	}
	source := req.AccessUnit
	source.AnnexB = nil
	if len(req.AccessUnit.AnnexB) > 0 {
		d.sources[d.serial] = source
	}
	if len(d.sources) > 32 {
		return fail(errors.New("decoder output backlog"))
	}
	var drain uint64
	if req.EndOfStream {
		drain = 1
		d.drained = true
	}
	status, err := call("decode", uint64(len(req.AccessUnit.AnnexB)), d.serial, drain)
	if err != nil {
		return fail(err)
	}
	if int32(status) != 0 {
		return fail(fmt.Errorf("codec status %d", int32(status)))
	}
	ptr, err = call("output_ptr")
	if err != nil {
		return fail(err)
	}
	header, ok := d.module.Memory().Read(uint32(ptr), 36)
	if !ok {
		return fail(errors.New("output bounds"))
	}
	word := func(i int) uint32 { return binary.LittleEndian.Uint32(header[i*4:]) }
	w, h := int(word(0)), int(word(1))
	if w == 0 {
		return DecodedFrame{Pending: true}, nil
	}
	if _, _, err := boundedDimensions(image.Rect(0, 0, w, h), req.Limits.withDefaults()); err != nil {
		return fail(err)
	}
	if w != d.width || h != d.height {
		return fail(errors.New("unannounced geometry change"))
	}
	token := uint64(word(7)) | uint64(word(8))<<32
	src, ok := d.sources[token]
	if !ok {
		return fail(errors.New("unknown output source"))
	}
	delete(d.sources, token)
	img := &streamImage{rect: image.Rect(0, 0, w, h), color: d.color}
	for i := range 3 {
		pw, ph := w, h
		stride := int(word(2))
		if i > 0 {
			pw, ph = (w+1)/2, (h+1)/2
			stride = int(word(3))
		}
		if stride < pw || stride > 16384 {
			return fail(errors.New("invalid plane stride"))
		}
		plane, ok := d.module.Memory().Read(word(4+i), uint32((ph-1)*stride+pw))
		if !ok {
			return fail(errors.New("invalid plane bounds"))
		}
		img.planes[i] = make([]byte, pw*ph)
		for y := range ph {
			copy(img.planes[i][y*pw:(y+1)*pw], plane[y*stride:y*stride+pw])
		}
	}
	return DecodedFrame{Image: img, Source: &src}, nil
}

// Reset/Close cancel and join the current execution; there is no detached decoder
// worker. Reset discards reference pictures while retaining compiled trusted code.
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
	if close {
		d.closeRuntime()
	} else {
		d.dropModule()
	}
	return nil
}
func (d *embeddedDecoder) dropModule() {
	if d.module != nil {
		_ = d.module.Close(context.Background())
	}
	d.module = nil
	d.drained = false
	d.sources = nil
	d.width, d.height = 0, 0
	d.color = streamColor{}
}
func (d *embeddedDecoder) closeRuntime() {
	d.dropModule()
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
