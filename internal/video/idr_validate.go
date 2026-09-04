package video

import (
	"bytes"
	"fmt"
)

// The host parser deliberately reads only a bounded SPS prefix. The full
// third-party SPS/PPS/entropy parsers run exclusively inside WASI.
func validateIDR(req DecodeRequest) (int, int, error) {
	l := req.Limits.withDefaults()
	if err := l.validate(); err != nil {
		return 0, 0, err
	}
	data := req.AccessUnit.AnnexB
	if len(data) > min(l.MaxAccessUnitBytes, defaultMaxAU) {
		return 0, 0, ErrAccessUnitTooLarge
	}
	bad := func() (int, int, error) {
		return 0, 0, fmt.Errorf("%w: expected one complete SPS/PPS/IDR access unit", ErrDecodeFailed)
	}
	var sps, pps, idr bool
	var sid, pid uint32
	var w, h int
	count := 0
	for len(data) != 0 {
		// Annex-B allows leading/trailing zero bytes and 3/4-byte start codes.
		i := bytes.Index(data, []byte{0, 0, 1})
		if i < 0 {
			return bad()
		}
		for _, b := range data[:i] {
			if b != 0 {
				return bad()
			}
		}
		data = data[i+3:]
		end := bytes.Index(data, []byte{0, 0, 1})
		if end < 0 {
			end = len(data)
		}
		nalu := bytes.TrimRight(data[:end], "\x00")
		data = data[end:]
		count++
		if count > min(l.MaxPacketsPerUnit, defaultMaxPackets) || len(nalu) < 2 || nalu[0]&0x80 != 0 {
			return bad()
		}
		switch nalu[0] & 31 {
		case 7:
			if sps || pps || idr || len(nalu) > 4096 {
				return bad()
			}
			var err error
			w, h, sid, err = parseSPSBounds(nalu, l)
			if err != nil {
				return 0, 0, err
			}
			sps = true
		case 8:
			if !sps || pps || idr || len(nalu) > 4096 {
				return bad()
			}
			b := newRBSP(nalu[1:])
			pid = b.ue()
			if pid > 255 || b.ue() != sid {
				return bad()
			}
			b.read(1)
			b.read(1)
			if b.ue() != 0 || b.err {
				return bad()
			} // FMO unsupported.
			pps = true
		case 5:
			if !sps || !pps || idr {
				return bad()
			} // hi264 returns the first slice only.
			// Only the bounded slice-header prefix is needed by the host.
			b := newRBSP(nalu[1:min(len(nalu), 128)])
			first, typ, ref := b.ue(), b.ue(), b.ue()
			if b.err || first != 0 || (typ != 2 && typ != 7) || ref != pid {
				return bad()
			}
			idr = true
		case 6, 9, 12: // SEI, AUD and filler cannot create a decoded picture.
		default:
			return bad() // Includes all non-IDR VCL and extension NALUs.
		}
	}
	if !idr {
		return bad()
	}
	return w, h, nil
}

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
	// hi264 reconstructs at (0,0) and rounds displayed size to macroblocks.
	// Accept only right/bottom cropping that preserves the coded macroblock grid.
	if left != 0 || top != 0 || right >= 16 || bottom >= 16 {
		return bad()
	}
	w, h := cw-right, ch-bottom
	if w > uint64(min(l.MaxWidth, defaultMaxWidth)) || h > uint64(min(l.MaxHeight, defaultMaxHeight)) || w*h > uint64(min(l.MaxPixels, int64(defaultMaxPixels))) || cw*ch > defaultMaxWidth*((defaultMaxHeight+15)/16*16) {
		return 0, 0, 0, ErrDimensionsExceeded
	}
	return int(w), int(h), sid, nil
}
