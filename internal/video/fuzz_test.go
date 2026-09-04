package video

import (
	"testing"
	"time"
)

func FuzzDepacketizerMalformedPayload(f *testing.F) {
	f.Add(uint16(1), uint32(1), true, []byte{0x65, 1})
	f.Add(uint16(2), uint32(2), true, []byte{0x7c, 0x85, 1})
	f.Add(uint16(3), uint32(3), true, []byte{0x78, 0, 4, 0x67})
	f.Fuzz(func(t *testing.T, sequence uint16, timestamp uint32, marker bool, payload []byte) {
		if len(payload) > 1024 {
			payload = payload[:1024]
		}
		depacketizer, err := NewDepacketizer(1, Limits{MaxPacketBytes: 1024, MaxAccessUnitBytes: 4096, MaxPacketsPerUnit: 8})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = depacketizer.Push(RTPPacket{
			Generation: 1, SequenceNumber: sequence, Timestamp: timestamp,
			Marker: marker, ReceivedAt: time.Unix(1, 0), Payload: payload,
		})
	})
}
