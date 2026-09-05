package video

import (
	"bytes"
	"fmt"
)

func (d *embeddedDecoder) validate(req DecodeRequest) error {
	l := req.Limits.withDefaults()
	if err := l.validate(); err != nil {
		return err
	}
	data := req.AccessUnit.AnnexB
	if len(data) == 0 && req.EndOfStream && d.module != nil {
		return nil
	}
	if len(data) > min(l.MaxAccessUnitBytes, defaultMaxAU) {
		return ErrAccessUnitTooLarge
	}
	bad := func() error { return fmt.Errorf("%w: invalid access unit or missing reference state", ErrDecodeFailed) }
	w, h := d.width, d.height
	col := d.color
	var sps, pps, vcl, idr bool
	count := 0
	for len(data) > 0 {
		i := bytes.Index(data, []byte{0, 0, 1})
		if i < 0 {
			return bad()
		}
		for _, v := range data[:i] {
			if v != 0 {
				return bad()
			}
		}
		data = data[i+3:]
		end := bytes.Index(data, []byte{0, 0, 1})
		if end < 0 {
			end = len(data)
		}
		n := bytes.TrimRight(data[:end], "\x00")
		data = data[end:]
		count++
		if len(n) < 2 || n[0]&0x80 != 0 || count > min(l.MaxPacketsPerUnit, defaultMaxPackets) {
			return bad()
		}
		switch n[0] & 31 {
		case 7:
			if sps || pps || vcl || len(n) > 4096 {
				return bad()
			}
			var err error
			w, h, _, err = parseSPSBoundsColor(n, l, &col)
			if err != nil {
				return err
			}
			sps = true
		case 8:
			if !sps || pps || vcl || len(n) > 4096 {
				return bad()
			}
			pps = true
		case 1, 5:
			isIDR := n[0]&31 == 5
			if vcl && isIDR != idr {
				return bad()
			}
			if isIDR && (!sps || !pps) {
				return bad()
			}
			b := newRBSP(n[1:min(len(n), 128)])
			first, typ, pid := b.ue(), b.ue(), b.ue()
			if b.err || typ > 9 || pid > 255 || (!vcl && first != 0) || (vcl && first == 0) {
				return bad()
			}
			if isIDR && typ%5 != 2 {
				return bad()
			}
			if !isIDR && (d.width == 0 || sps || pps) {
				return bad()
			}
			vcl, idr = true, isIDR
		case 6, 9, 12:
		default:
			return bad()
		}
	}
	if !vcl || idr != req.AccessUnit.Keyframe || w == 0 || h == 0 {
		return bad()
	}
	if w > l.MaxWidth || h > l.MaxHeight || int64(w)*int64(h) > l.MaxPixels {
		return ErrDimensionsExceeded
	}
	if d.width != 0 && (w != d.width || h != d.height || col != d.color) {
		d.dropModule()
	}
	d.width, d.height, d.color = w, h, col
	return nil
}
