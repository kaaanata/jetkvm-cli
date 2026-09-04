package jetkvm

import (
	"context"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

// RequestNegotiatedVideoKeyframe uses the remote track identity established by
// SDP. OnTrack is deliberately not a prerequisite: Pion normally fires it only
// after first RTP, while a cold sender may wait for PLI before sending RTP.
func (s *Session) RequestNegotiatedVideoKeyframe(ctx context.Context, generation uint64) error {
	if generation != s.generation {
		return ErrSessionReplaced
	}
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	select {
	case <-s.Done():
		return s.Err()
	default:
	}
	var ssrc uint32
	for _, receiver := range s.peer.GetReceivers() {
		for _, track := range receiver.Tracks() {
			if track.Kind() != webrtc.RTPCodecTypeVideo {
				continue
			}
			if track.SSRC() == 0 || ssrc != 0 {
				return ErrWebRTCFailed
			}
			ssrc = uint32(track.SSRC())
		}
	}
	if ssrc == 0 {
		return ErrWebRTCFailed
	}
	return s.RequestVideoKeyframe(ctx, generation, ssrc)
}

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
