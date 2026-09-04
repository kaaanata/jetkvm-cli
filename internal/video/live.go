package video

import (
	"context"
	"errors"
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
// one newest complete IDR in addition to the one being decoded. Non-IDR AUs
// return ErrVideoUnavailable; packet-loss/size errors retain their taxonomy.
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
		p.pendingIDR = nil
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
		if !p.resetting {
			unit = p.pendingIDR
			p.pendingIDR = nil
		}
		p.mu.Unlock()
		if unit == nil {
			select {
			case <-p.liveCtx.Done():
				return
			case <-p.liveWake:
				continue
			}
		}
		p.pushMu.Lock()
		_, err := p.decodeUnit(p.liveCtx, unit)
		p.pushMu.Unlock()
		p.mu.Lock()
		if !p.closed && !p.resetting && unit.Generation == p.generation {
			if err != nil {
				p.liveError = errors.Join(ErrDecodeFailed, err)
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
