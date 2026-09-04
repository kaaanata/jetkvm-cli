package automation

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"net"
	"strings"
	"time"

	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/input"
	"github.com/kaaanata/jetkvm-cli/internal/jetkvm"
	"github.com/kaaanata/jetkvm-cli/internal/video"
)

type screenSession interface {
	Observe(context.Context, time.Duration) (ScreenObservation, error)
}

func (s *Service) Observe(ctx context.Context, request ObserveRequest) (ScreenObservation, error) {
	if _, err := s.authorize("jetkvm_observe", request.DeviceID, request.Scope, nil, false); err != nil {
		return ScreenObservation{}, err
	}
	var result ScreenObservation
	err := s.registry.Execute(ctx, request.DeviceID, request.Ref, "video", func(ctx context.Context, session control.Session) error {
		observer, ok := session.(screenSession)
		if !ok {
			return domain.ErrCapabilityUnavailable
		}
		var err error
		result, err = observer.Observe(ctx, request.Freshness)
		return err
	})
	return result, err
}

func (s *sessionAdapter) startVideo(session *jetkvm.Session) error {
	decoder, err := video.EmbeddedDecoder().New()
	if err != nil {
		return err
	}
	pipeline, err := video.NewPipeline(string(s.deviceID), s.generation, video.Limits{}, decoder, s)
	if err != nil {
		return errors.Join(err, decoder.Close())
	}
	s.video = pipeline
	ctx, cancel := context.WithCancel(context.Background())
	if err := pipeline.StartLive(ctx); err != nil {
		cancel()
		return errors.Join(err, pipeline.Close())
	}
	s.videoCancel = cancel
	s.videoDone = make(chan struct{})
	s.trackReady = make(chan struct{})
	go func() {
		defer close(s.videoDone)
		defer pipeline.Close()
		select {
		case <-ctx.Done():
			return
		case <-session.Done():
			return
		case track, ok := <-session.VideoTracks():
			if !ok || track.Track == nil || !strings.EqualFold(track.Track.Codec().MimeType, video.CodecH264) {
				return
			}
			s.videoSSRC = uint32(track.Track.SSRC())
			close(s.trackReady)
			for ctx.Err() == nil {
				if err := track.Track.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
					return
				}
				packet, _, err := track.Track.ReadRTP()
				if err != nil {
					if networkErr, ok := errors.AsType[net.Error](err); ok && networkErr.Timeout() {
						continue
					}
					return
				}
				_ = pipeline.PushLive(ctx, video.RTPPacket{Generation: s.generation, SequenceNumber: packet.SequenceNumber, Timestamp: packet.Timestamp, Marker: packet.Marker, ReceivedAt: time.Now(), Payload: packet.Payload})
			}
		}
	}()
	return nil
}

func (s *sessionAdapter) stopVideo() {
	if s.videoCancel != nil {
		s.videoCancel()
		<-s.videoDone
	}
}

func (s *sessionAdapter) RequestPLI(ctx context.Context, generation uint64) error {
	if err := s.checkGeneration(generation); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-s.videoDone:
		return video.ErrVideoUnavailable
	case <-s.trackReady:
	}
	session, ok := s.protocol.(*jetkvm.Session)
	if !ok {
		return video.ErrVideoUnavailable
	}
	return session.RequestVideoKeyframe(ctx, session.Generation(), s.videoSSRC)
}

func (s *sessionAdapter) Observe(ctx context.Context, freshness time.Duration) (ScreenObservation, error) {
	if s.video == nil {
		return ScreenObservation{}, domain.ErrCapabilityUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	observation, err := s.video.AwaitObservation(ctx, video.ObserveRequest{Generation: s.generation, Freshness: freshness, NotBefore: time.Now()})
	if err != nil {
		return ScreenObservation{}, err
	}
	var data bytes.Buffer
	if err := png.Encode(&data, observation.Image); err != nil {
		return ScreenObservation{}, err
	}
	result := ScreenObservation{Observation: *observation, MIMEType: "image/png", Data: data.Bytes()}
	s.observationsMu.Lock()
	// Retain only metadata for a small bounded set of issued bindings.
	metadata := result
	metadata.Data = nil
	metadata.Observation.Image = nil
	s.observations = append(s.observations, metadata)
	if len(s.observations) > 16 {
		s.observations = s.observations[len(s.observations)-16:]
	}
	s.observationsMu.Unlock()
	return result, nil
}

func (s *sessionAdapter) Capture(ctx context.Context, generation uint64) (input.Observation, error) {
	if err := s.checkGeneration(generation); err != nil {
		return input.Observation{}, err
	}
	result, err := s.Observe(ctx, 0)
	if err == nil {
		s.observationsMu.Lock()
		s.captured = &result
		s.observationsMu.Unlock()
	}
	return input.Observation{ID: result.Observation.ID, Generation: result.Observation.Frame.Generation}, err
}

func (s *sessionAdapter) lastCapture() *ScreenObservation {
	s.observationsMu.Lock()
	defer s.observationsMu.Unlock()
	result := s.captured
	s.captured = nil
	return result
}

func batchHasScreenshot(batch input.Batch) bool {
	for _, action := range batch.Actions {
		if action.Type == input.ActionScreenshot {
			return true
		}
	}
	return false
}

func (s *sessionAdapter) resolveObservation(binding *input.ObservationBinding) (*input.ObservationBinding, error) {
	if binding == nil {
		return nil, nil
	}
	s.observationsMu.Lock()
	defer s.observationsMu.Unlock()
	for _, item := range s.observations {
		o := item.Observation
		if binding.ID == o.ID && binding.Generation == o.Frame.Generation && (binding.Width == 0 || binding.Width == o.Frame.Width) && (binding.Height == 0 || binding.Height == o.Frame.Height) && (binding.CapturedAt.IsZero() || binding.CapturedAt.Equal(o.CapturedAt)) {
			if s.video != nil && !s.video.MatchesGeometry(o.Frame.Generation, o.Frame.Width, o.Frame.Height) {
				return nil, input.ErrObservationStale
			}
			if time.Since(o.CapturedAt) > input.DefaultLimits().MaxObservationAge {
				return nil, input.ErrObservationStale
			}
			return &input.ObservationBinding{ID: o.ID, Generation: o.Frame.Generation, Width: o.Frame.Width, Height: o.Frame.Height, CapturedAt: o.CapturedAt}, nil
		}
	}
	return nil, input.ErrObservationStale
}
