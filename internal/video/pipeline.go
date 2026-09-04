package video

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"sync"
	"time"
)

// KeyframeRequester requests an RTCP Picture Loss Indication for one fenced
// control generation.
type KeyframeRequester interface {
	RequestPLI(context.Context, uint64) error
}

// Pipeline serializes depacketization and decoding while allowing concurrent
// consumers to wait for the latest immutable observation.
type Pipeline struct {
	mu           sync.Mutex
	deviceID     string
	generation   uint64
	limits       Limits
	depacketizer *Depacketizer
	decoder      Decoder
	requester    KeyframeRequester
	latest       *Observation
	notify       chan struct{}
	closed       bool
	frameID      uint64
	lastPLI      time.Time
	now          func() time.Time
	newID        func() (string, error)
}

// NewPipeline constructs a generation-fenced video pipeline.
func NewPipeline(deviceID string, generation uint64, limits Limits, decoder Decoder, requester KeyframeRequester) (*Pipeline, error) {
	limits = limits.withDefaults()
	if deviceID == "" || decoder == nil || generation == 0 || limits.validate() != nil {
		return nil, ErrInvalidConfig
	}
	depacketizer, err := NewDepacketizer(generation, limits)
	if err != nil {
		return nil, err
	}
	return &Pipeline{
		deviceID: deviceID, generation: generation, limits: limits,
		depacketizer: depacketizer, decoder: decoder, requester: requester,
		notify: make(chan struct{}), now: time.Now, newID: observationID,
	}, nil
}

// Push receives one RTP packet. It publishes only successfully decoded IDR
// observations; compressed-frame receipt alone never becomes an observation.
func (p *Pipeline) Push(ctx context.Context, packet RTPPacket) (*Observation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, ErrPipelineClosed
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	accessUnit, err := p.depacketizer.Push(packet)
	if err != nil {
		return nil, err
	}
	if accessUnit == nil {
		return nil, nil
	}
	if !accessUnit.Decodable {
		return nil, ErrVideoUnavailable
	}
	decoded, err := p.decoder.Decode(ctx, DecodeRequest{AccessUnit: *accessUnit, Limits: p.limits})
	if err != nil {
		if contextErr := context.Cause(ctx); contextErr != nil {
			return nil, contextErr
		}
		return nil, fmt.Errorf("%w: %w", ErrDecodeFailed, err)
	}
	if decoded.Image == nil {
		return nil, ErrDecodeFailed
	}
	width, height, err := boundedDimensions(decoded.Image.Bounds(), p.limits)
	if err != nil {
		return nil, err
	}
	now := p.now()
	id, err := p.newID()
	if err != nil {
		return nil, fmt.Errorf("create observation ID: %w", err)
	}
	p.frameID++
	observation := &Observation{
		ID: id, DeviceID: p.deviceID, CapturedAt: now, Trust: ObservationTrust,
		Frame: FrameMetadata{
			FrameID: p.frameID, Generation: p.generation, RTPTime: accessUnit.RTPTime,
			FirstSequence: accessUnit.FirstSequence, LastSequence: accessUnit.LastSequence,
			ReceivedAt: accessUnit.ReceivedAt, DecodedAt: now, Width: width, Height: height,
			Codec: CodecH264, Keyframe: accessUnit.Keyframe, Discontinuity: accessUnit.Discontinuity,
		},
		Image: decoded.Image,
	}
	p.latest = observation
	close(p.notify)
	p.notify = make(chan struct{})
	return cloneObservation(observation), nil
}

// AwaitObservation returns a matching fresh frame or waits for one. When a
// frame is absent or stale it requests one rate-limited PLI before waiting.
func (p *Pipeline) AwaitObservation(ctx context.Context, request ObserveRequest) (*Observation, error) {
	if request.Freshness < 0 {
		return nil, ErrInvalidConfig
	}
	for {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, ErrPipelineClosed
		}
		if request.Generation != p.generation {
			p.mu.Unlock()
			return nil, ErrGenerationMismatch
		}
		freshness := request.Freshness
		if freshness == 0 {
			freshness = p.limits.DefaultFreshness
		}
		now := p.now()
		hadFrame := p.latest != nil
		if hadFrame && !p.latest.CapturedAt.After(now) && now.Sub(p.latest.CapturedAt) <= freshness {
			observation := cloneObservation(p.latest)
			p.mu.Unlock()
			return observation, nil
		}
		notify := p.notify
		shouldPLI := p.requester != nil && (p.lastPLI.IsZero() || now.Sub(p.lastPLI) >= p.limits.MinPLIInterval)
		if shouldPLI {
			p.lastPLI = now
		}
		requester := p.requester
		generation := p.generation
		p.mu.Unlock()

		if shouldPLI {
			if err := requester.RequestPLI(ctx, generation); err != nil {
				return nil, fmt.Errorf("request H.264 keyframe: %w", err)
			}
		}
		select {
		case <-ctx.Done():
			cause := context.Cause(ctx)
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				if hadFrame {
					return nil, errors.Join(cause, ErrFrameStale)
				}
				return nil, errors.Join(cause, ErrVideoUnavailable)
			}
			if cause != nil {
				return nil, cause
			}
			return nil, ctx.Err()
		case <-notify:
		}
	}
}

// Reset fences all previous frames and packet state to a new generation.
func (p *Pipeline) Reset(generation uint64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrPipelineClosed
	}
	if generation == 0 || generation == p.generation {
		return ErrInvalidConfig
	}
	if err := p.decoder.Reset(); err != nil {
		return fmt.Errorf("reset H.264 decoder: %w", err)
	}
	if err := p.depacketizer.Reset(generation); err != nil {
		return err
	}
	p.generation = generation
	p.latest = nil
	p.lastPLI = time.Time{}
	close(p.notify)
	p.notify = make(chan struct{})
	return nil
}

// Close wakes waiters and closes the decoder exactly once.
func (p *Pipeline) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	close(p.notify)
	p.mu.Unlock()
	return p.decoder.Close()
}

func boundedDimensions(bounds image.Rectangle, limits Limits) (int, int, error) {
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 || width > limits.MaxWidth || height > limits.MaxHeight || int64(width) > limits.MaxPixels/int64(height) {
		return 0, 0, ErrDimensionsExceeded
	}
	return width, height, nil
}

func cloneObservation(source *Observation) *Observation {
	if source == nil {
		return nil
	}
	clone := *source
	return &clone
}

func observationID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "obs_" + hex.EncodeToString(random[:]), nil
}
