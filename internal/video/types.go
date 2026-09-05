// Package video owns the bounded JetKVM H.264 receive and observation pipeline.
package video

import (
	"errors"
	"image"
	"time"
)

var (
	ErrInvalidConfig      = errors.New("invalid video configuration")
	ErrInvalidPacket      = errors.New("invalid H.264 RTP packet")
	ErrUnsupportedPacket  = errors.New("unsupported H.264 RTP packetization mode")
	ErrPacketLoss         = errors.New("H.264 RTP packet loss")
	ErrPacketTooLarge     = errors.New("H.264 RTP packet exceeds configured limit")
	ErrAccessUnitTooLarge = errors.New("H.264 access unit exceeds configured limit")
	ErrGenerationMismatch = errors.New("video generation mismatch")
	ErrDecoderUnavailable = errors.New("embedded H.264 decoder is unavailable")
	ErrDecodeFailed       = errors.New("H.264 decode failed")
	ErrDimensionsExceeded = errors.New("decoded frame dimensions exceed configured limits")
	ErrFrameStale         = errors.New("video frame is stale")
	ErrVideoUnavailable   = errors.New("video frame is unavailable")
	ErrPipelineClosed     = errors.New("video pipeline is closed")
)

const (
	CodecH264          = "video/H264"
	ObservationTrust   = "untrusted_observation"
	defaultMaxPacket   = 64 << 10
	defaultMaxAU       = 8 << 20
	defaultMaxPackets  = 8192
	defaultMaxWidth    = 4096
	defaultMaxHeight   = 2160
	defaultMaxPixels   = 4096 * 2160
	defaultFreshness   = 5 * time.Second
	defaultPLIInterval = 250 * time.Millisecond
)

// Limits are enforced before retaining compressed input and again after decode.
// Decoder implementations must also apply them before allocating output planes.
type Limits struct {
	MaxPacketBytes     int
	MaxAccessUnitBytes int
	MaxPacketsPerUnit  int
	MaxWidth           int
	MaxHeight          int
	MaxPixels          int64
	DefaultFreshness   time.Duration
	MinPLIInterval     time.Duration
}

func (l Limits) withDefaults() Limits {
	if l.MaxPacketBytes == 0 {
		l.MaxPacketBytes = defaultMaxPacket
	}
	if l.MaxAccessUnitBytes == 0 {
		l.MaxAccessUnitBytes = defaultMaxAU
	}
	if l.MaxPacketsPerUnit == 0 {
		l.MaxPacketsPerUnit = defaultMaxPackets
	}
	if l.MaxWidth == 0 {
		l.MaxWidth = defaultMaxWidth
	}
	if l.MaxHeight == 0 {
		l.MaxHeight = defaultMaxHeight
	}
	if l.MaxPixels == 0 {
		l.MaxPixels = defaultMaxPixels
	}
	if l.DefaultFreshness == 0 {
		l.DefaultFreshness = defaultFreshness
	}
	if l.MinPLIInterval == 0 {
		l.MinPLIInterval = defaultPLIInterval
	}
	return l
}

func (l Limits) validate() error {
	if l.MaxPacketBytes <= 0 || l.MaxAccessUnitBytes <= 0 || l.MaxPacketsPerUnit <= 0 ||
		l.MaxWidth <= 0 || l.MaxHeight <= 0 || l.MaxPixels <= 0 ||
		l.DefaultFreshness <= 0 || l.MinPLIInterval <= 0 {
		return ErrInvalidConfig
	}
	return nil
}

// RTPPacket is the transport-neutral subset of an RTP packet required by the
// H.264 receiver. The WebRTC adapter remains responsible for RTP parsing.
type RTPPacket struct {
	Generation     uint64
	SequenceNumber uint16
	Timestamp      uint32
	Marker         bool
	ReceivedAt     time.Time
	Payload        []byte
}

// AccessUnit is one complete Annex-B H.264 access unit.
type AccessUnit struct {
	chain               uint64 // Pipeline-owned reference-chain epoch.
	Generation          uint64
	RTPTime             uint32
	FirstSequence       uint16
	LastSequence        uint16
	ReceivedAt          time.Time
	AnnexB              []byte
	HasSPS              bool
	HasPPS              bool
	Keyframe            bool
	Decodable           bool
	ParameterSetsReused bool
	Discontinuity       bool
}

// DecodeRequest carries immutable compressed bytes and mandatory allocation
// bounds into a decoder backend.
type DecodeRequest struct {
	AccessUnit  AccessUnit
	EndOfStream bool // Explicit finite-fixture drain; never set by live ingestion.
	Limits      Limits
}

// DecodedFrame is the decoder-independent image result.
type DecodedFrame struct {
	Image   image.Image
	Source  *AccessUnit // Original input associated with reordered output.
	Pending bool        // Input accepted, decoder needs another picture before output.
}

// FrameMetadata identifies exactly which stateful stream produced an image.
type FrameMetadata struct {
	FrameID       uint64    `json:"frame_id"`
	Generation    uint64    `json:"generation"`
	RTPTime       uint32    `json:"rtp_timestamp"`
	FirstSequence uint16    `json:"first_sequence"`
	LastSequence  uint16    `json:"last_sequence"`
	ReceivedAt    time.Time `json:"received_at"`
	DecodedAt     time.Time `json:"decoded_at"`
	Width         int       `json:"width"`
	Height        int       `json:"height"`
	Codec         string    `json:"codec"`
	Keyframe      bool      `json:"keyframe"`
	Discontinuity bool      `json:"discontinuity,omitzero"`
}

// Observation is a fresh, generation-fenced frame suitable for a later
// observation-bound input operation. Image remains process-local and is not
// part of durable metadata.
type Observation struct {
	ID         string        `json:"observation_id"`
	DeviceID   string        `json:"device_id"`
	CapturedAt time.Time     `json:"captured_at"`
	Trust      string        `json:"trust"`
	Frame      FrameMetadata `json:"frame"`
	Image      image.Image   `json:"-"`
}

// ObserveRequest defines the freshness and generation contract for a capture.
type ObserveRequest struct {
	Generation uint64
	Freshness  time.Duration
	// NotBefore requires the frame's first RTP receive timestamp to be at or
	// after this boundary. Set it at capture invocation for observe-after.
	// Zero preserves the normal cached-frame behavior.
	NotBefore time.Time
}
