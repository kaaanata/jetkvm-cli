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

// EmbeddedDecoder continuously decodes H.264 I/P/B pictures in a bounded WASI
// reactor, with reference state owned by one video session.
func EmbeddedDecoder() DecoderFactory { return embeddedDecoderFactory{} }
