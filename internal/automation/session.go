package automation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/input"
	"github.com/kaaanata/jetkvm-cli/internal/jetkvm"
)

var (
	ErrClientNotFound    = errors.New("JetKVM client was not found for device")
	ErrUnexpectedSession = errors.New("control session does not implement the automation runtime")
)

// SessionFactory maps stable device identities to preconfigured JetKVM
// clients. It never creates clients or resolves credentials during Open.
type SessionFactory struct {
	clients       map[domain.DeviceID]*jetkvm.Client
	sessionConfig jetkvm.SessionConfig
}

func NewSessionFactory(clients map[domain.DeviceID]*jetkvm.Client, sessionConfig jetkvm.SessionConfig) (*SessionFactory, error) {
	if len(clients) == 0 {
		return nil, errors.New("at least one JetKVM client is required")
	}
	copy := make(map[domain.DeviceID]*jetkvm.Client, len(clients))
	for deviceID, client := range clients {
		if deviceID == "" || client == nil {
			return nil, errors.New("JetKVM client map contains an empty identity or nil client")
		}
		copy[deviceID] = client
	}
	return &SessionFactory{clients: copy, sessionConfig: sessionConfig}, nil
}

func (f *SessionFactory) Open(ctx context.Context, deviceID domain.DeviceID, generation uint64, capabilities []string) (control.Session, error) {
	client := f.clients[deviceID]
	if client == nil {
		return nil, ErrClientNotFound
	}
	protocolSession, err := client.OpenSession(ctx, f.sessionConfig)
	if err != nil {
		return nil, err
	}
	adapter := &sessionAdapter{
		deviceID:     deviceID,
		generation:   generation,
		capabilities: slices.Clone(capabilities),
		protocol:     protocolSession,
	}
	manager, err := input.NewManager(input.ManagerConfig{
		Transport:  adapter,
		Generation: generation,
		// A decoder-backed observer is intentionally absent until the
		// production decoder acceptance gate is satisfied.
		Observer: nil,
	})
	if err != nil {
		return nil, errors.Join(err, protocolSession.CloseContext(context.Background()))
	}
	adapter.input = manager
	return adapter, nil
}

type sendBarrier struct {
	once    sync.Once
	start   func(context.Context) error
	started atomic.Bool
	errMu   sync.Mutex
	err     error
}

func (b *sendBarrier) cross(ctx context.Context) error {
	b.once.Do(func() {
		err := b.start(ctx)
		b.errMu.Lock()
		b.err = err
		b.errMu.Unlock()
		if err == nil {
			b.started.Store(true)
		}
	})
	b.errMu.Lock()
	defer b.errMu.Unlock()
	return b.err
}

type sessionAdapter struct {
	deviceID     domain.DeviceID
	generation   uint64
	capabilities []string
	protocol     protocolSession
	input        *input.Manager
	closed       atomic.Bool
	operationMu  sync.Mutex
	barrier      *sendBarrier
	neutralized  bool
}

type protocolSession interface {
	Generation() uint64
	SendHIDForGeneration(context.Context, uint64, []byte) error
	FlushHID(context.Context, uint64) error
	CallRPC(context.Context, string, any, any) error
	CloseContext(context.Context) error
}

func (s *sessionAdapter) Close(ctx context.Context) error {
	return s.close(ctx)
}

func (s *sessionAdapter) Disconnect(ctx context.Context) error {
	return s.close(ctx)
}

func (s *sessionAdapter) close(ctx context.Context) error {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	if s.closed.Load() {
		return nil
	}
	if !s.neutralized {
		cleanupCtx, cancel := context.WithTimeoutCause(context.Background(), 2*time.Second, input.ErrNeutralization)
		defer cancel()
		if err := s.input.Reconcile(cleanupCtx, s.generation); err != nil {
			return err
		}
		s.neutralized = true
	}
	if err := s.protocol.CloseContext(ctx); err != nil {
		return err
	}
	s.closed.Store(true)
	return nil
}

func (s *sessionAdapter) RunActions(ctx context.Context, batch input.Batch, start func(context.Context) error) (input.BatchReceipt, bool, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	if s.closed.Load() {
		return input.BatchReceipt{}, false, jetkvm.ErrSessionClosed
	}
	barrier := &sendBarrier{start: start}
	s.barrier = barrier
	receipt, err := s.input.RunActions(ctx, s.generation, batch)
	s.barrier = nil
	return receipt, barrier.started.Load(), err
}

func (s *sessionAdapter) ReleaseInput(ctx context.Context, start func(context.Context) error) (bool, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	if s.closed.Load() {
		return false, jetkvm.ErrSessionClosed
	}
	barrier := &sendBarrier{start: start}
	s.barrier = barrier
	err := s.input.Reconcile(ctx, s.generation)
	s.barrier = nil
	if err == nil {
		s.neutralized = true
	}
	return barrier.started.Load(), err
}

func (s *sessionAdapter) CallRPC(ctx context.Context, method string, params, result any) error {
	if s.closed.Load() {
		return jetkvm.ErrSessionClosed
	}
	return s.protocol.CallRPC(ctx, method, params, result)
}

func (s *sessionAdapter) SendHID(ctx context.Context, generation uint64, _ input.Reliability, payload []byte) error {
	if err := s.checkGeneration(generation); err != nil {
		return err
	}
	if err := s.crossBarrier(ctx); err != nil {
		return err
	}
	if err := s.checkGeneration(generation); err != nil {
		return err
	}
	return s.protocol.SendHIDForGeneration(ctx, s.protocol.Generation(), payload)
}

func (s *sessionAdapter) SendWheel(ctx context.Context, generation uint64, y, x int8) error {
	if err := s.checkGeneration(generation); err != nil {
		return err
	}
	if err := s.crossBarrier(ctx); err != nil {
		return err
	}
	if err := s.checkGeneration(generation); err != nil {
		return err
	}
	return s.protocol.CallRPC(ctx, "wheelReport", struct {
		WheelY int8 `json:"wheelY"`
		WheelX int8 `json:"wheelX"`
	}{WheelY: y, WheelX: x}, nil)
}

func (s *sessionAdapter) Flush(ctx context.Context, generation uint64) error {
	if err := s.checkGeneration(generation); err != nil {
		return err
	}
	return s.protocol.FlushHID(ctx, s.protocol.Generation())
}

func (s *sessionAdapter) crossBarrier(ctx context.Context) error {
	if s.barrier == nil {
		return nil
	}
	return s.barrier.cross(ctx)
}

func (s *sessionAdapter) checkGeneration(generation uint64) error {
	if s.closed.Load() {
		return jetkvm.ErrSessionClosed
	}
	if generation != s.generation {
		return input.ErrStaleGeneration
	}
	return nil
}

type runtimeSession interface {
	control.Session
	RunActions(context.Context, input.Batch, func(context.Context) error) (input.BatchReceipt, bool, error)
	CallRPC(context.Context, string, any, any) error
}

type inputReleaseSession interface {
	ReleaseInput(context.Context, func(context.Context) error) (bool, error)
}

func automationSession(session control.Session) (runtimeSession, error) {
	adapter, ok := session.(runtimeSession)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedSession, session)
	}
	return adapter, nil
}

var (
	_ control.Session    = (*sessionAdapter)(nil)
	_ input.HIDTransport = (*sessionAdapter)(nil)
	_ protocolSession    = (*jetkvm.Session)(nil)
)
