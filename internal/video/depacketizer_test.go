package video

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestDepacketizerReordersFUAAndTracksIDR(t *testing.T) {
	d, err := NewDepacketizer(7, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	packets := []RTPPacket{
		packet(7, 101, 99, false, now, []byte{0x7c, 0x05, 0x22}),
		packet(7, 100, 99, false, now, []byte{0x7c, 0x85, 0x11}),
		packet(7, 102, 99, true, now, []byte{0x7c, 0x45, 0x33}),
	}
	for index, input := range packets {
		unit, err := d.Push(input)
		if err != nil {
			t.Fatalf("Push(%d) error: %v", index, err)
		}
		if index < len(packets)-1 && unit != nil {
			t.Fatalf("Push(%d) emitted early", index)
		}
		if index == len(packets)-1 {
			if unit == nil || !unit.Keyframe || unit.Decodable {
				t.Fatalf("unit = %+v", unit)
			}
			want := append(annexBStartCode[:], []byte{0x65, 0x11, 0x22, 0x33}...)
			if !bytes.Equal(unit.AnnexB, want) {
				t.Fatalf("AnnexB = %x, want %x", unit.AnnexB, want)
			}
		}
	}
}

func TestDepacketizerCachesSPSPPSForLaterIDR(t *testing.T) {
	d, err := NewDepacketizer(1, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	stap := []byte{0x78, 0, 2, 0x67, 0x11, 0, 2, 0x68, 0x22}
	unit, err := d.Push(packet(1, 10, 1, true, now, stap))
	if err != nil || unit == nil || !unit.HasSPS || !unit.HasPPS {
		t.Fatalf("parameter-set unit = (%+v, %v)", unit, err)
	}
	unit, err = d.Push(packet(1, 11, 2, true, now, []byte{0x65, 0xaa}))
	if err != nil {
		t.Fatal(err)
	}
	if unit == nil || !unit.Decodable || !unit.ParameterSetsReused || !unit.HasSPS || !unit.HasPPS {
		t.Fatalf("IDR unit = %+v", unit)
	}
	if bytes.Count(unit.AnnexB, annexBStartCode[:]) != 3 {
		t.Fatalf("AnnexB NAL count = %d", bytes.Count(unit.AnnexB, annexBStartCode[:]))
	}
}

func TestDepacketizerDetectsLossOnTimestampAdvance(t *testing.T) {
	d, err := NewDepacketizer(1, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_, _ = d.Push(packet(1, 20, 5, false, now, []byte{0x7c, 0x85, 1}))
	_, _ = d.Push(packet(1, 22, 5, true, now, []byte{0x7c, 0x45, 3}))
	unit, err := d.Push(packet(1, 23, 6, true, now, []byte{0x61, 4}))
	if err != nil {
		t.Fatalf("timestamp advance error = %v", err)
	}
	if unit == nil || !unit.Discontinuity {
		t.Fatalf("new access unit = %+v, want discontinuity", unit)
	}
}

func TestDepacketizerReportsLossWhileRetainingNextUnit(t *testing.T) {
	d, err := NewDepacketizer(1, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_, _ = d.Push(packet(1, 20, 5, false, now, []byte{0x7c, 0x85, 1}))
	_, _ = d.Push(packet(1, 22, 5, true, now, []byte{0x7c, 0x45, 3}))
	if _, err := d.Push(packet(1, 23, 6, false, now, []byte{0x61, 4})); !errors.Is(err, ErrPacketLoss) {
		t.Fatalf("timestamp advance error = %v, want packet loss", err)
	}
	unit, err := d.Push(packet(1, 24, 6, true, now, []byte{0x61, 5}))
	if err != nil || unit == nil {
		t.Fatalf("retained next unit = (%+v, %v)", unit, err)
	}
}

func TestDepacketizerRejectsOversizedInput(t *testing.T) {
	d, err := NewDepacketizer(1, Limits{MaxPacketBytes: 4, MaxAccessUnitBytes: 7})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Push(packet(1, 1, 1, true, time.Now(), []byte{1, 2, 3, 4, 5})); !errors.Is(err, ErrPacketTooLarge) {
		t.Fatalf("packet error = %v", err)
	}
	if _, err := d.Push(packet(1, 2, 2, true, time.Now(), []byte{0x65, 1, 2, 3})); !errors.Is(err, ErrAccessUnitTooLarge) {
		t.Fatalf("access unit error = %v", err)
	}
}

func packet(generation uint64, sequence uint16, timestamp uint32, marker bool, received time.Time, payload []byte) RTPPacket {
	return RTPPacket{Generation: generation, SequenceNumber: sequence, Timestamp: timestamp, Marker: marker, ReceivedAt: received, Payload: payload}
}
