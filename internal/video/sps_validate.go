package video

import (
	"fmt"
)

// The host reads only a bounded SPS prefix before the sandbox allocates frames.
// Full entropy and reference-picture parsing stays inside the WASI codec.
type rbspBits struct {
	data []byte
	pos  int
	err  bool
}

func newRBSP(src []byte) *rbspBits {
	b := &rbspBits{data: make([]byte, 0, len(src))}
	zeros := 0
	for _, v := range src {
		if zeros == 2 && v == 3 {
			zeros = 0
			continue
		}
		b.data = append(b.data, v)
		if v == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return b
}
func (b *rbspBits) read(n int) uint32 {
	if b.err || n > 32 || b.pos+n > len(b.data)*8 {
		b.err = true
		return 0
	}
	var v uint32
	for range n {
		v = v<<1 | uint32((b.data[b.pos/8]>>(7-b.pos%8))&1)
		b.pos++
	}
	return v
}
func (b *rbspBits) ue() uint32 {
	zeros := 0
	for !b.err && b.read(1) == 0 {
		zeros++
		if zeros > 30 {
			b.err = true
			return 0
		}
	}
	return (1 << zeros) - 1 + b.read(zeros)
}
func (b *rbspBits) se() int64 {
	u := b.ue()
	if u&1 != 0 {
		return int64(u+1) / 2
	}
	return -int64(u) / 2
}

func parseSPSBounds(nalu []byte, l Limits) (int, int, uint32, error) {
	return parseSPSBoundsColor(nalu, l, nil)
}
func parseSPSBoundsColor(nalu []byte, l Limits, col *streamColor) (int, int, uint32, error) {
	bad := func() (int, int, uint32, error) {
		return 0, 0, 0, fmt.Errorf("%w: unsupported or malformed SPS", ErrDecodeFailed)
	}
	b := newRBSP(nalu[1:])
	profile := b.read(8)
	b.read(8)
	b.read(8)
	sid := b.ue()
	if sid > 31 {
		return bad()
	}
	switch profile {
	case 66, 77, 88:
	case 100:
		if b.ue() != 1 || b.ue() != 0 || b.ue() != 0 {
			return bad()
		} // 8-bit 4:2:0 only.
		if b.read(1) != 0 {
			return bad()
		} // Transform bypass is unsupported.
		if b.read(1) != 0 {
			for i := range 8 {
				if b.read(1) == 0 {
					continue
				}
				last, next := int64(8), int64(8)
				n := 16
				if i >= 6 {
					n = 64
				}
				for range n {
					if next != 0 {
						next = (last + b.se() + 256) % 256
					}
					if next != 0 {
						last = next
					}
				}
			}
		}
	default:
		return bad()
	}
	if b.ue() > 12 {
		return bad()
	}
	switch b.ue() {
	case 0:
		if b.ue() > 12 {
			return bad()
		}
	case 1:
		b.read(1)
		b.se()
		b.se()
		n := b.ue()
		if n > 255 {
			return bad()
		}
		for range n {
			b.se()
		}
	case 2:
	default:
		return bad()
	}
	if b.ue() > 16 {
		return bad()
	}
	b.read(1)
	cw, ch := (uint64(b.ue())+1)*16, (uint64(b.ue())+1)*16
	if b.read(1) != 1 {
		return bad()
	} // Progressive frames only.
	b.read(1)
	var left, right, top, bottom uint64
	if b.read(1) != 0 {
		left, right, top, bottom = uint64(b.ue())*2, uint64(b.ue())*2, uint64(b.ue())*2, uint64(b.ue())*2
	}
	if b.err || left+right >= cw || top+bottom >= ch {
		return bad()
	}
	// Preserve the supported progressive capture geometry: bounded right/bottom
	// cropping only, without changing the origin of observation coordinates.
	if left != 0 || top != 0 || right >= 16 || bottom >= 16 {
		return bad()
	}
	w, h := cw-right, ch-bottom
	if w > uint64(min(l.MaxWidth, defaultMaxWidth)) || h > uint64(min(l.MaxHeight, defaultMaxHeight)) || w*h > uint64(min(l.MaxPixels, int64(defaultMaxPixels))) || cw*ch > defaultMaxWidth*((defaultMaxHeight+15)/16*16) {
		return 0, 0, 0, ErrDimensionsExceeded
	}
	if col != nil {
		*col = streamColor{}
		if b.read(1) != 0 {
			if b.read(1) != 0 {
				if b.read(8) == 255 {
					b.read(16)
					b.read(16)
				}
			}
			if b.read(1) != 0 {
				b.read(1)
			}
			if b.read(1) != 0 {
				b.read(3)
				col.full = b.read(1) != 0
				if b.read(1) != 0 {
					b.read(8)
					b.read(8)
					col.matrix = b.read(8)
				}
			}
		}
		if b.err {
			return bad()
		}
		if col.matrix != 0 && col.matrix != 1 && col.matrix != 2 && col.matrix != 5 && col.matrix != 6 && col.matrix != 9 {
			return bad()
		}
	}
	return int(w), int(h), sid, nil
}
