package video

import "context"

// Decoder is the narrow boundary for a bounded, in-process H.264 decoder.
// Implementations must be safe against hostile input, honor cancellation, and
// apply request limits before allocating decoded planes.
type Decoder interface {
	Name() string
	Decode(context.Context, DecodeRequest) (DecodedFrame, error)
	Reset() error
	Close() error
}

// DecoderFactory reports availability separately from construction so CLI and
// MCP inventories never advertise screenshots without a working backend.
type DecoderFactory interface {
	Name() string
	Available() bool
	New() (Decoder, error)
}

type unavailableDecoderFactory struct{}

func (unavailableDecoderFactory) Name() string          { return "unavailable" }
func (unavailableDecoderFactory) Available() bool       { return false }
func (unavailableDecoderFactory) New() (Decoder, error) { return nil, ErrDecoderUnavailable }

// EmbeddedDecoder returns the current production decoder factory. No backend
// is selected until it satisfies the release, cancellation, and HIL gates in
// DECODER_DECISION.md.
func EmbeddedDecoder() DecoderFactory { return unavailableDecoderFactory{} }
