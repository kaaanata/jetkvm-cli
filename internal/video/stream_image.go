package video

import (
	"image"
	"image/color"
)

type streamColor struct {
	matrix uint32
	full   bool
}

// streamImage owns copied planes. Decoder calls can never mutate issued pixels.
// Color conversion is deferred until an observation is actually encoded.
type streamImage struct {
	rect   image.Rectangle
	planes [3][]byte
	color  streamColor
}

func (*streamImage) ColorModel() color.Model   { return color.NRGBAModel }
func (p *streamImage) Bounds() image.Rectangle { return p.rect }
func (p *streamImage) At(x, y int) color.Color {
	if !image.Pt(x, y).In(p.rect) {
		return color.NRGBA{}
	}
	w := p.rect.Dx()
	ci := (y/2)*((w+1)/2) + x/2
	yy := int(p.planes[0][y*w+x])
	u := int(p.planes[1][ci]) - 128
	v := int(p.planes[2][ci]) - 128
	scale, rv, gu, gv, bu := 298, 409, 100, 208, 516
	if p.color.full {
		scale, rv, gu, gv, bu = 256, 359, 88, 183, 454
	} else {
		yy -= 16
	}
	if p.color.matrix == 1 {
		rv, gu, gv, bu = 459, 55, 136, 541
		if p.color.full {
			rv, gu, gv, bu = 403, 48, 120, 475
		}
	}
	if p.color.matrix == 9 {
		rv, gu, gv, bu = 430, 48, 167, 548
		if p.color.full {
			rv, gu, gv, bu = 377, 42, 146, 482
		}
	}
	clamp := func(v int) uint8 { return uint8(min(255, max(0, (v+128)>>8))) }
	return color.NRGBA{R: clamp(scale*yy + rv*v), G: clamp(scale*yy - gu*u - gv*v), B: clamp(scale*yy + bu*u), A: 255}
}
