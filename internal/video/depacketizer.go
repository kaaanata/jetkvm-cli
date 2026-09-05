package video

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"slices"
)

const (
	naluTypeMask  = 0x1f
	naluTypeSPS   = 7
	naluTypePPS   = 8
	naluTypeIDR   = 5
	naluTypeSTAPA = 24
	naluTypeFUA   = 28
	fuStart       = 0x80
	fuEnd         = 0x40
)

var annexBStartCode = [...]byte{0, 0, 0, 1}

type pendingUnit struct {
	generation uint64
	timestamp  uint32
	packets    map[uint16]RTPPacket
	marker     *uint16
	bytes      int
}

// Depacketizer implements RFC 6184 single-NAL, STAP-A, and FU-A packetization
// with bounded out-of-order buffering. It is intentionally transport-neutral.
type Depacketizer struct {
	generation    uint64
	limits        Limits
	pending       *pendingUnit
	sps           []byte
	pps           []byte
	lastSequence  uint16
	lastTimestamp uint32
	haveLast      bool
	discontinuity bool
}

// NewDepacketizer creates a receiver fenced to one control generation.
func NewDepacketizer(generation uint64, limits Limits) (*Depacketizer, error) {
	limits = limits.withDefaults()
	if generation == 0 || limits.validate() != nil {
		return nil, ErrInvalidConfig
	}
	return &Depacketizer{generation: generation, limits: limits}, nil
}

// Reset discards buffered packets and parameter sets and moves to a new
// generation. Old-generation packets can never complete a new access unit.
func (d *Depacketizer) Reset(generation uint64) error {
	if generation == 0 {
		return ErrInvalidConfig
	}
	d.generation = generation
	d.pending = nil
	d.sps = nil
	d.pps = nil
	d.haveLast = false
	d.discontinuity = false
	return nil
}

// Push buffers one packet and emits an access unit only after a contiguous
// sequence through the marker packet has arrived.
func (d *Depacketizer) Push(packet RTPPacket) (*AccessUnit, error) {
	if packet.Generation != d.generation {
		return nil, ErrGenerationMismatch
	}
	if packet.ReceivedAt.IsZero() || len(packet.Payload) == 0 {
		return nil, ErrInvalidPacket
	}
	if len(packet.Payload) > d.limits.MaxPacketBytes {
		return nil, ErrPacketTooLarge
	}

	// Completed timestamps and sequences fence duplicates/late packets, including wrap.
	if d.haveLast && int32(packet.Timestamp-d.lastTimestamp) <= 0 {
		return nil, nil
	}
	if d.pending != nil && int32(packet.Timestamp-d.pending.timestamp) < 0 {
		return nil, nil
	}
	var priorLoss bool
	if d.pending == nil || d.pending.timestamp != packet.Timestamp {
		priorLoss = d.pending != nil
		if priorLoss {
			d.discontinuity = true
		}
		d.pending = &pendingUnit{
			generation: packet.Generation,
			timestamp:  packet.Timestamp,
			packets:    make(map[uint16]RTPPacket),
		}
	}
	unit := d.pending
	if _, duplicate := unit.packets[packet.SequenceNumber]; duplicate {
		return nil, nil
	}
	unit.bytes += len(packet.Payload)
	if unit.bytes > d.limits.MaxAccessUnitBytes || len(unit.packets) >= d.limits.MaxPacketsPerUnit {
		d.pending = nil
		d.discontinuity = true
		return nil, ErrAccessUnitTooLarge
	}
	packet.Payload = bytes.Clone(packet.Payload)
	unit.packets[packet.SequenceNumber] = packet
	if packet.Marker {
		sequence := packet.SequenceNumber
		unit.marker = &sequence
	}
	if unit.marker == nil {
		if priorLoss {
			return nil, ErrPacketLoss
		}
		return nil, nil
	}

	ordered, complete := orderedPackets(unit, d.limits.MaxPacketsPerUnit)
	if !complete {
		return nil, nil
	}
	accessUnit, err := d.assemble(unit, ordered)
	if err != nil {
		d.pending = nil
		d.discontinuity = true
		return nil, err
	}
	d.pending = nil
	accessUnit.Discontinuity = d.discontinuity || (d.haveLast && accessUnit.FirstSequence != d.lastSequence+1)
	d.lastSequence, d.lastTimestamp, d.haveLast = accessUnit.LastSequence, accessUnit.RTPTime, true
	d.discontinuity = false
	return accessUnit, nil
}

func orderedPackets(unit *pendingUnit, maxPackets int) ([]RTPPacket, bool) {
	marker := *unit.marker
	sequences := make([]uint16, 0, len(unit.packets))
	for sequence := range unit.packets {
		if int(uint16(marker-sequence)) >= maxPackets {
			continue
		}
		sequences = append(sequences, sequence)
	}
	if len(sequences) == 0 {
		return nil, false
	}
	slices.SortFunc(sequences, func(a, b uint16) int {
		return int(uint16(marker-b)) - int(uint16(marker-a))
	})
	packets := make([]RTPPacket, 0, len(sequences))
	for index, sequence := range sequences {
		if index > 0 && sequence != sequences[index-1]+1 {
			return nil, false
		}
		packets = append(packets, unit.packets[sequence])
	}
	if packets[len(packets)-1].SequenceNumber != marker {
		return nil, false
	}
	if isFUAPayload(packets[0].Payload) && packets[0].Payload[1]&fuStart == 0 {
		return nil, false
	}
	return packets, true
}

func (d *Depacketizer) assemble(unit *pendingUnit, packets []RTPPacket) (*AccessUnit, error) {
	var annexB []byte
	firstReceived := packets[0].ReceivedAt
	var hasSPS, hasPPS, keyframe, picture bool
	var fragmented bool
	var fragmentType byte

	appendNAL := func(nalu []byte) error {
		if len(nalu) == 0 || len(annexB)+len(annexBStartCode)+len(nalu) > d.limits.MaxAccessUnitBytes {
			return ErrAccessUnitTooLarge
		}
		typeID := nalu[0] & naluTypeMask
		switch typeID {
		case naluTypeSPS:
			d.sps = bytes.Clone(nalu)
			hasSPS = true
		case naluTypePPS:
			d.pps = bytes.Clone(nalu)
			hasPPS = true
		case naluTypeIDR:
			keyframe = true
			picture = true
		case 1:
			picture = true
		}
		annexB = append(annexB, annexBStartCode[:]...)
		annexB = append(annexB, nalu...)
		return nil
	}

	for _, packet := range packets {
		if packet.ReceivedAt.Before(firstReceived) {
			firstReceived = packet.ReceivedAt
		}
		payload := packet.Payload
		typeID := payload[0] & naluTypeMask
		switch {
		case typeID > 0 && typeID < naluTypeSTAPA:
			if fragmented {
				return nil, ErrInvalidPacket
			}
			if err := appendNAL(payload); err != nil {
				return nil, err
			}
		case typeID == naluTypeSTAPA:
			if fragmented {
				return nil, ErrInvalidPacket
			}
			for offset := 1; offset < len(payload); {
				if len(payload)-offset < 2 {
					return nil, ErrInvalidPacket
				}
				size := int(binary.BigEndian.Uint16(payload[offset : offset+2]))
				offset += 2
				if size == 0 || size > len(payload)-offset {
					return nil, ErrInvalidPacket
				}
				if err := appendNAL(payload[offset : offset+size]); err != nil {
					return nil, err
				}
				offset += size
			}
		case typeID == naluTypeFUA:
			if len(payload) < 3 || payload[1]&0x20 != 0 {
				return nil, ErrInvalidPacket
			}
			start := payload[1]&fuStart != 0
			end := payload[1]&fuEnd != 0
			if start {
				if fragmented || end {
					return nil, ErrInvalidPacket
				}
				fragmented = true
				fragmentType = payload[1] & naluTypeMask
				header := payload[0]&0xe0 | fragmentType
				annexB = append(annexB, annexBStartCode[:]...)
				annexB = append(annexB, header)
			} else if !fragmented || fragmentType != payload[1]&naluTypeMask {
				return nil, ErrPacketLoss
			}
			if len(annexB)+len(payload)-2 > d.limits.MaxAccessUnitBytes {
				return nil, ErrAccessUnitTooLarge
			}
			annexB = append(annexB, payload[2:]...)
			if end {
				fragmented = false
				if fragmentType == 1 || fragmentType == 5 {
					picture = true
				}
				if fragmentType == naluTypeIDR {
					keyframe = true
				}
			}
		default:
			return nil, fmt.Errorf("%w: NAL unit type %d", ErrUnsupportedPacket, typeID)
		}
	}
	if fragmented {
		return nil, ErrPacketLoss
	}

	reused := false
	if keyframe && (!hasSPS || !hasPPS) {
		if len(d.sps) == 0 || len(d.pps) == 0 {
			return &AccessUnit{
				Generation: unit.generation, RTPTime: unit.timestamp,
				FirstSequence: packets[0].SequenceNumber, LastSequence: packets[len(packets)-1].SequenceNumber,
				ReceivedAt: firstReceived, AnnexB: annexB, HasSPS: hasSPS, HasPPS: hasPPS, Keyframe: true,
			}, nil
		}
		if len(d.sps)+len(d.pps)+2*len(annexBStartCode)+len(annexB) > d.limits.MaxAccessUnitBytes {
			return nil, ErrAccessUnitTooLarge
		}
		prefix := make([]byte, 0, len(d.sps)+len(d.pps)+2*len(annexBStartCode)+len(annexB))
		prefix = append(prefix, annexBStartCode[:]...)
		prefix = append(prefix, d.sps...)
		prefix = append(prefix, annexBStartCode[:]...)
		prefix = append(prefix, d.pps...)
		annexB = append(prefix, annexB...)
		hasSPS, hasPPS, reused = true, true, true
	}

	return &AccessUnit{
		Generation: unit.generation, RTPTime: unit.timestamp,
		FirstSequence: packets[0].SequenceNumber, LastSequence: packets[len(packets)-1].SequenceNumber,
		ReceivedAt: firstReceived, AnnexB: annexB, HasSPS: hasSPS, HasPPS: hasPPS,
		Keyframe: keyframe, Decodable: picture && ((keyframe && hasSPS && hasPPS) || (!keyframe && len(d.sps) > 0 && len(d.pps) > 0)), ParameterSetsReused: reused,
	}, nil
}

func isFUAPayload(payload []byte) bool {
	return len(payload) >= 2 && payload[0]&naluTypeMask == naluTypeFUA
}
