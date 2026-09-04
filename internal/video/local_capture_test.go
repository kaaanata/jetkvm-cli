package video

import (
	"image/png"
	"os"
	"testing"
	"time"
)

// TestEmbeddedDecoderLocalCapture keeps private captures outside testdata.
func TestEmbeddedDecoderLocalCapture(t *testing.T) {
	path := os.Getenv("JETKVM_H264_FIXTURE")
	if path == "" {
		t.Skip("set JETKVM_H264_FIXTURE for local HIL")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	d, err := EmbeddedDecoder().New()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for i := range 2 {
		start := time.Now()
		f, err := d.Decode(t.Context(), DecodeRequest{AccessUnit: AccessUnit{AnnexB: data, Keyframe: true, Decodable: true}})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("decode %d: %dx%d in %v (first includes WASM compilation)", i, f.Image.Bounds().Dx(), f.Image.Bounds().Dy(), time.Since(start))
		if output := os.Getenv("JETKVM_H264_PNG"); output != "" && i == 0 {
			file, err := os.Create(output)
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(file, f.Image)
			closeErr := file.Close()
			if err != nil {
				t.Fatal(err)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
		}
	}
}
