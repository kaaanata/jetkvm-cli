package video

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"os"
	"testing"
	"time"
)

func fixtureRequest(t *testing.T, name string) DecodeRequest {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name + ".h264")
	if err != nil {
		t.Fatal(err)
	}
	return DecodeRequest{AccessUnit: AccessUnit{AnnexB: data, Keyframe: true, Decodable: true}}
}

func TestEmbeddedDecoderRealIDR(t *testing.T) {
	d, err := EmbeddedDecoder().New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	for _, name := range []string{"red-baseline", "red-high"} {
		t.Run(name, func(t *testing.T) {
			f, err := d.Decode(t.Context(), fixtureRequest(t, name))
			if err != nil {
				t.Fatal(err)
			}
			if f.Image.Bounds().Dx() != 32 || f.Image.Bounds().Dy() != 32 {
				t.Fatal(f.Image.Bounds())
			}
			for y := range 32 {
				for x := range 32 {
					r, g, b, a := f.Image.At(x, y).RGBA()
					// FFmpeg reference decodes this solid red IDR as RGB (254,0,0).
					// Permit two levels for independent YUV/RGB rounding conventions.
					if r>>8 < 251 || g>>8 > 2 || b>>8 > 2 || a != 65535 {
						t.Fatalf("pixel %d,%d: %d,%d,%d,%d", x, y, r>>8, g>>8, b>>8, a)
					}
				}
			}
		})
	}
	if err := d.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Decode(t.Context(), fixtureRequest(t, "red-baseline")); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Decode(t.Context(), fixtureRequest(t, "red-baseline")); !errors.Is(err, ErrPipelineClosed) {
		t.Fatal(err)
	}
}

func TestEmbeddedDecoderGradientGolden(t *testing.T) {
	d, _ := EmbeddedDecoder().New()
	t.Cleanup(func() { _ = d.Close() })
	f, err := d.Decode(t.Context(), fixtureRequest(t, "gradient-high"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open("testdata/gradient-high.png")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	want, err := png.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	if f.Image.Bounds() != want.Bounds() {
		t.Fatal(f.Image.Bounds())
	}
	for y := range 32 {
		for x := range 32 {
			r, g, b, a := f.Image.At(x, y).RGBA()
			wr, wg, wb, wa := want.At(x, y).RGBA()
			for i, p := range [][2]uint32{{r, wr}, {g, wg}, {b, wb}, {a, wa}} {
				delta := int(p[0]>>8) - int(p[1]>>8)
				if delta < -2 || delta > 2 {
					t.Fatalf("%d,%d channel %d: got %d want %d", x, y, i, p[0]>>8, p[1]>>8)
				}
			}
		}
	}
}

func TestEmbeddedDecoderInputBounds(t *testing.T) {
	d, _ := EmbeddedDecoder().New()
	t.Cleanup(func() { _ = d.Close() })
	for _, tc := range []struct {
		name string
		req  DecodeRequest
		want error
	}{
		{"empty", DecodeRequest{}, ErrDecodeFailed},
		{"malformed", DecodeRequest{AccessUnit: AccessUnit{AnnexB: []byte{0, 0, 1, 0x67, 0xff}}}, ErrDecodeFailed},
		{"width", func() DecodeRequest { r := fixtureRequest(t, "red-baseline"); r.Limits.MaxWidth = 16; return r }(), ErrDimensionsExceeded},
		{"pixels", func() DecodeRequest { r := fixtureRequest(t, "red-baseline"); r.Limits.MaxPixels = 1023; return r }(), ErrDimensionsExceeded},
		{"au", func() DecodeRequest {
			r := fixtureRequest(t, "red-baseline")
			r.Limits.MaxAccessUnitBytes = 10
			return r
		}(), ErrAccessUnitTooLarge},
		{"negative", DecodeRequest{Limits: Limits{MaxWidth: -1}}, ErrInvalidConfig},
		{"multiple", func() DecodeRequest {
			r := fixtureRequest(t, "red-baseline")
			r.AccessUnit.AnnexB = append(r.AccessUnit.AnnexB, r.AccessUnit.AnnexB...)
			return r
		}(), ErrDecodeFailed},
		{"non-idr", DecodeRequest{AccessUnit: AccessUnit{AnnexB: []byte{0, 0, 1, 0x41, 0x80}}}, ErrDecodeFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := d.Decode(t.Context(), tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
	if d.(*embeddedDecoder).runtime != nil {
		t.Fatal("invalid/oversized input reached WASM allocation")
	}
}

func TestEmbeddedDecoderCancellation(t *testing.T) {
	d, _ := EmbeddedDecoder().New()
	t.Cleanup(func() { _ = d.Close() })
	r := fixtureRequest(t, "red-high")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := d.Decode(ctx, r); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Cancellation during trusted-module compilation is reported once compilation
	// joins. Actual hostile execution cancellation is tested with an infinite
	// module in TestSandboxExecutionCancellationAndJoin; race slows compilation.
	ctx, cancel = context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	if _, err := d.Decode(ctx, r); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := d.Decode(t.Context(), r); err != nil {
		t.Fatalf("reuse after cancellation: %v", err)
	}
}

func FuzzValidateIDR(f *testing.F) {
	data, err := os.ReadFile("testdata/red-high.h264")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(data)
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _, _ = validateIDR(DecodeRequest{AccessUnit: AccessUnit{AnnexB: data}})
	})
}

func TestEmbeddedDecoderTruncatedCABAC(t *testing.T) {
	d, _ := EmbeddedDecoder().New()
	t.Cleanup(func() { _ = d.Close() })
	r := fixtureRequest(t, "gradient-high")
	idx := bytes.LastIndex(r.AccessUnit.AnnexB, []byte{0, 0, 1})
	if idx < 0 {
		t.Fatal("missing IDR")
	}
	r.AccessUnit.AnnexB = r.AccessUnit.AnnexB[:idx+12]
	if _, _, err := validateIDR(r); err != nil {
		t.Fatalf("test must reach sandbox: %v", err)
	}
	if _, err := d.Decode(t.Context(), r); !errors.Is(err, ErrDecodeFailed) {
		t.Fatalf("truncated CABAC returned success: %v", err)
	}
}
