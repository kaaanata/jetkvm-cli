package video

import (
	"context"
	"errors"
	"time"
)

// StartLive selects asynchronous ingestion for this Pipeline's lifetime.
// Call once before the RTP read loop. The context belongs to the stream, not
// an individual observation. Close cancels and joins the only decoder worker.
// Synchronous Push and live PushLive cannot be mixed on one pipeline.
func (p *Pipeline) StartLive(ctx context.Context) error {
	p.lifecycleMu.Lock()
	defer p.lifecycleMu.Unlock()
	p.pushMu.Lock()
	defer p.pushMu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrPipelineClosed
	}
	if p.live {
		return ErrInvalidConfig
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.live = true
	p.liveCtx, p.liveCancel = context.WithCancel(ctx)
	p.liveWake = make(chan struct{}, 1)
	p.liveDone = make(chan struct{})
	go p.runLive()
	return nil
}

// PushLive assembles RTP without waiting for decoder work. It retains at most
// a bounded ordered reference chain. Only decoded display frames are replaced;
// loss or overload discards the chain until an independent IDR arrives.
// Successful enqueue is not a decoded-frame receipt: use AwaitObservation.
func (p *Pipeline) PushLive(ctx context.Context, packet RTPPacket) error {
	_, err := p.receive(ctx, packet, true)
	return err
}

// wakeLive must be called with mu held.
func (p *Pipeline) wakeLive() {
	if p.liveWake != nil {
		select {
		case p.liveWake <- struct{}{}:
		default:
		}
	}
}

func (p *Pipeline) runLive() {
	defer close(p.liveDone)
	defer func() {
		p.mu.Lock()
		p.pending = nil
		p.pendingBytes = 0
		if !p.closed {
			close(p.notify)
			p.notify = make(chan struct{})
		}
		p.mu.Unlock()
	}()
	for {
		p.mu.Lock()
		if p.closed || p.liveCtx.Err() != nil {
			p.mu.Unlock()
			return
		}
		var unit *AccessUnit
		if !p.resetting && len(p.pending) > 0 {
			unit = p.pending[0]
			p.pending[0] = nil
			p.pending = p.pending[1:]
			p.pendingBytes -= len(unit.AnnexB)
		}
		p.mu.Unlock()
		if unit == nil {
			// The stream-owned worker also requests recovery while no observer is waiting.
			// Await and ingestion share lastPLI under mu; there is no second retry owner.
			p.mu.Lock()
			requester := p.requester
			generation := p.generation
			needsRecovery := !p.synced && requester != nil && !p.resetting
			delay := max(time.Millisecond, p.limits.MinPLIInterval-p.now().Sub(p.lastPLI))
			request := needsRecovery && (p.lastPLI.IsZero() || p.now().Sub(p.lastPLI) >= p.limits.MinPLIInterval)
			if request {
				p.lastPLI = p.now()
				delay = p.limits.MinPLIInterval
			}
			p.mu.Unlock()
			if request {
				_ = requester.RequestPLI(p.liveCtx, generation)
			}
			var retry <-chan time.Time
			var timer *time.Timer
			if needsRecovery {
				timer = time.NewTimer(delay)
				retry = timer.C
			}
			select {
			case <-p.liveCtx.Done():
				if timer != nil {
					timer.Stop()
				}
				return
			case <-p.liveWake:
			case <-retry:
			}
			if timer != nil {
				timer.Stop()
			}
			continue
		}
		p.pushMu.Lock()
		_, err := p.decodeUnit(p.liveCtx, unit)
		p.pushMu.Unlock()
		p.mu.Lock()
		if !p.closed && !p.resetting && unit.Generation == p.generation && unit.chain == p.chain {
			if err != nil {
				p.liveError = errors.Join(ErrDecodeFailed, err)
				p.breakChain()
			} else {
				p.liveError = nil
			}
			p.liveErrorAt = unit.ReceivedAt
			close(p.notify)
			p.notify = make(chan struct{})
		}
		p.mu.Unlock()
	}
}
