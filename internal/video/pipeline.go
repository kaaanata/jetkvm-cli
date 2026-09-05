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

	"github.com/kaaanata/jetkvm-cli/internal/progress"
)

// KeyframeRequester requests an RTCP Picture Loss Indication for one fenced
// control generation.
type KeyframeRequester interface {
	RequestPLI(context.Context, uint64) error
}

// Pipeline serializes depacketization and decoding while allowing concurrent
// consumers to wait for the latest immutable observation.
type Pipeline struct {
	pushMu       sync.Mutex // Owns decoder execution.
	receiveMu    sync.Mutex // Owns depacketizer; never held during live decoding.
	lifecycleMu  sync.Mutex // Serializes Reset and Close, never held by Await.
	mu           sync.Mutex
	decodeCancel context.CancelFunc
	resetting    bool
	live         bool
	liveCtx      context.Context
	liveCancel   context.CancelFunc
	liveDone     chan struct{}
	liveWake     chan struct{}
	pendingIDR   *AccessUnit
	liveError    error
	liveErrorAt  time.Time
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
	p.pushMu.Lock()
	defer p.pushMu.Unlock()
	accessUnit, err := p.receive(ctx, packet, false)
	if err != nil || accessUnit == nil {
		return nil, err
	}
	return p.decodeUnit(ctx, accessUnit)
}

// receive serializes only bounded packet assembly. Complete AUs own their bytes.
func (p *Pipeline) receive(ctx context.Context, packet RTPPacket, live bool) (*AccessUnit, error) {
	p.receiveMu.Lock()
	defer p.receiveMu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, ErrPipelineClosed
	}
	if p.live != live {
		return nil, ErrInvalidConfig
	}
	if p.resetting {
		return nil, ErrVideoUnavailable
	}
	if live && p.liveCtx.Err() != nil {
		return nil, errors.Join(ErrVideoUnavailable, p.liveCtx.Err())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	accessUnit, err := p.depacketizer.Push(packet)
	if err != nil {
		return nil, err
	}
	if accessUnit == nil {
		return nil, nil
	}
	if !accessUnit.Keyframe || !accessUnit.Decodable {
		return nil, ErrVideoUnavailable
	}
	if live {
		// Replace only complete IDRs, never drop arbitrary packets from an AU.
		p.pendingIDR = accessUnit
		p.wakeLive()
	}
	return accessUnit, nil
}

// decodeUnit is called with pushMu held, outside the receive lock.
func (p *Pipeline) decodeUnit(ctx context.Context, accessUnit *AccessUnit) (*Observation, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrPipelineClosed
	}
	if p.resetting || accessUnit.Generation != p.generation {
		p.mu.Unlock()
		return nil, ErrGenerationMismatch
	}
	decodeCtx, cancel := context.WithCancel(ctx)
	p.decodeCancel = cancel
	p.mu.Unlock()
	defer func() { cancel(); p.mu.Lock(); p.decodeCancel = nil; p.mu.Unlock() }()
	decoded, err := p.decoder.Decode(decodeCtx, DecodeRequest{AccessUnit: *accessUnit, Limits: p.limits})
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, ErrPipelineClosed
	}
	if p.resetting || accessUnit.Generation != p.generation {
		return nil, ErrGenerationMismatch
	}
	if err != nil {
		if contextErr := context.Cause(decodeCtx); contextErr != nil {
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
		ID: id, DeviceID: p.deviceID, CapturedAt: accessUnit.ReceivedAt, Trust: ObservationTrust,
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
// frame is absent, stale, or before NotBefore, it retries rate-limited PLIs
// even when no new RTP packets arrive to wake the waiter.
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
		if p.live && p.liveCtx.Err() != nil {
			err := errors.Join(ErrVideoUnavailable, p.liveCtx.Err())
			p.mu.Unlock()
			return nil, err
		}
		freshness := request.Freshness
		if freshness == 0 {
			freshness = p.limits.DefaultFreshness
		}
		now := p.now()
		hadFrame := p.latest != nil
		if hadFrame && (request.NotBefore.IsZero() || !p.latest.Frame.ReceivedAt.Before(request.NotBefore)) &&
			!p.latest.Frame.ReceivedAt.IsZero() && !p.latest.Frame.ReceivedAt.After(now) && now.Sub(p.latest.Frame.ReceivedAt) <= freshness {
			observation := cloneObservation(p.latest)
			p.mu.Unlock()
			return observation, nil
		}
		if p.liveError != nil && (request.NotBefore.IsZero() || !p.liveErrorAt.Before(request.NotBefore)) {
			err := p.liveError
			p.mu.Unlock()
			return nil, err
		}
		notify := p.notify
		shouldPLI := p.requester != nil && (!p.live || (p.decodeCancel == nil && p.pendingIDR == nil)) && (p.lastPLI.IsZero() || now.Sub(p.lastPLI) >= p.limits.MinPLIInterval)
		if shouldPLI {
			p.lastPLI = now
		}
		pliDelay := max(time.Millisecond, p.limits.MinPLIInterval-now.Sub(p.lastPLI))
		requester := p.requester
		generation := p.generation
		decoding := p.decodeCancel != nil
		p.mu.Unlock()
		if decoding {
			progress.Stage(ctx, "Decoding screen")
		} else {
			progress.Stage(ctx, "Waiting for a fresh screen")
		}

		if shouldPLI {
			if err := requester.RequestPLI(ctx, generation); err != nil {
				return nil, fmt.Errorf("request H.264 keyframe: %w", err)
			}
		}
		var retry <-chan time.Time
		var timer *time.Timer
		if requester != nil {
			timer = time.NewTimer(pliDelay)
			retry = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
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
			if timer != nil {
				timer.Stop()
			}
		case <-retry:
		}
	}
}

// Reset fences all previous frames and packet state to a new generation.
func (p *Pipeline) Reset(generation uint64) error {
	p.lifecycleMu.Lock()
	defer p.lifecycleMu.Unlock()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrPipelineClosed
	}
	if generation == 0 || generation == p.generation {
		p.mu.Unlock()
		return ErrInvalidConfig
	}
	p.resetting = true
	p.latest = nil
	p.pendingIDR = nil
	p.liveError = nil
	p.liveErrorAt = time.Time{}
	if p.decodeCancel != nil {
		p.decodeCancel()
	}
	close(p.notify)
	p.notify = make(chan struct{})
	p.mu.Unlock()
	p.pushMu.Lock()
	defer p.pushMu.Unlock()
	p.receiveMu.Lock()
	defer p.receiveMu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	defer func() { p.resetting = false; close(p.notify); p.notify = make(chan struct{}); p.wakeLive() }()
	if err := p.decoder.Reset(); err != nil {
		return fmt.Errorf("reset H.264 decoder: %w", err)
	}
	if err := p.depacketizer.Reset(generation); err != nil {
		return err
	}
	p.generation = generation
	p.latest = nil
	p.lastPLI = time.Time{}
	return nil
}

// Close wakes waiters and closes the decoder exactly once.
func (p *Pipeline) Close() error {
	p.lifecycleMu.Lock()
	defer p.lifecycleMu.Unlock()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.pendingIDR = nil
	if p.liveCancel != nil {
		p.liveCancel()
	}
	if p.decodeCancel != nil {
		p.decodeCancel()
	}
	close(p.notify)
	liveDone := p.liveDone
	p.mu.Unlock()
	if liveDone != nil {
		<-liveDone
	}
	p.pushMu.Lock()
	defer p.pushMu.Unlock()
	return p.decoder.Close()
}

// MatchesGeometry checks a coordinate binding against the latest decoded
// geometry. Compressed SPS receipt alone does not establish a new geometry.
func (p *Pipeline) MatchesGeometry(generation uint64, width, height int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.closed && !p.resetting && p.latest != nil && width > 0 && height > 0 &&
		generation == p.generation && generation == p.latest.Frame.Generation &&
		width == p.latest.Frame.Width && height == p.latest.Frame.Height
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
