// Command wasmdecoder is a one-shot WASI stdin Annex-B to stdout RGBA shim.
package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/Eyevinn/hi264/pkg/decoder"
	"github.com/Eyevinn/hi264/pkg/yuv"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	data, err := io.ReadAll(io.LimitReader(os.Stdin, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return fmt.Errorf("invalid input length")
	}
	f, err := decoder.New().DecodeAnnexB(data)
	if err != nil {
		return err
	}
	if f == nil || f.Width <= 0 || f.Height <= 0 || f.Width > 4096 || f.Height > 2160 {
		return fmt.Errorf("invalid output dimensions")
	}
	cs, rng := yuv.BT601, yuv.LimitedRange
	if f.ColorDescriptionValid {
		cs = yuv.ColorSpaceFromMatrixCoefficients(f.MatrixCoefficients)
	}
	if f.VideoFullRangeFlag {
		rng = yuv.FullRange
	}
	img := yuv.FrameToImageCS(f, cs, rng)
	var header [8]byte
	binary.LittleEndian.PutUint32(header[:4], uint32(f.Width))
	binary.LittleEndian.PutUint32(header[4:], uint32(f.Height))
	if _, err := os.Stdout.Write(header[:]); err != nil {
		return err
	}
	_, err = os.Stdout.Write(img.Pix)
	return err
}
