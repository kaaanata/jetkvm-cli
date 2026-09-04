package jetkvm

import (
	"context"
	"github.com/pion/rtcp"
)

// RequestVideoKeyframe sends PLI on the exact session that owns the track.
func (s *Session) RequestVideoKeyframe(ctx context.Context, generation uint64, ssrc uint32) error {
	if generation != s.generation {
		return ErrSessionReplaced
	}
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-s.Done():
		return s.Err()
	default:
	}
	return s.peer.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: ssrc}})
}
