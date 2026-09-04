package input

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

var (
	ErrLeaseBusy          = errors.New("input lease is already held")
	ErrStaleGeneration    = errors.New("input generation is stale")
	ErrLeaseReleased      = errors.New("input lease is released")
	ErrInputUncertain     = errors.New("input state is uncertain")
	ErrNeutralization     = errors.New("input neutralization could not be confirmed")
	ErrObservationMissing = errors.New("screenshot observer is unavailable")
)

type Reliability uint8

const (
	Reliable Reliability = iota
	Motion
)

// HIDTransport is the device actor's narrow input transport. SendHID carries
// HID-RPC v1 frames. SendWheel is separate because JetKVM 0.5.8/dev exposes
// wheelReport through JSON-RPC rather than handling HID-RPC message 0x04.
// Implementations must fence generation at their final send boundary.
type HIDTransport interface {
	SendHID(context.Context, uint64, Reliability, []byte) error
	SendWheel(context.Context, uint64, int8, int8) error
	Flush(context.Context, uint64) error
}

type ScreenshotObserver interface {
	Capture(context.Context, uint64) (Observation, error)
}

type Observation struct {
	ID         string
	Generation uint64
}

type State string

const (
	StateReady     State = "ready"
	StateUncertain State = "uncertain"
)

// GenerationToken binds every input operation to one control generation and
// one exclusive lease. Its opaque nonce prevents a stale caller from reusing
// another lease in the same generation.
type GenerationToken struct {
	generation uint64
	nonce      [16]byte
}

func (t GenerationToken) Generation() uint64 { return t.generation }

type ManagerConfig struct {
	Transport      HIDTransport
	Observer       ScreenshotObserver
	Generation     uint64
	Limits         Limits
	CleanupTimeout time.Duration
	Random         io.Reader
	Now            func() time.Time
}

type Manager struct {
	mu             sync.Mutex
	transport      HIDTransport
	observer       ScreenshotObserver
	generation     uint64
	active         [16]byte
	activeCancel   context.CancelCauseFunc
	leaseHeld      bool
	uncertain      bool
	limits         Limits
	cleanupTimeout time.Duration
	random         io.Reader
	now            func() time.Time
	lastPointer    Point
}

func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.Transport == nil || cfg.Generation == 0 {
		return nil, errors.New("input manager requires transport and non-zero generation")
	}
	limits := cfg.Limits
	if limits == (Limits{}) {
		limits = DefaultLimits()
	}
	if err := validateLimits(limits); err != nil {
		return nil, err
	}
	cleanupTimeout := cfg.CleanupTimeout
	if cleanupTimeout == 0 {
		cleanupTimeout = 2 * time.Second
	}
	if cleanupTimeout < 0 {
		return nil, errors.New("cleanup timeout must be positive")
	}
	random := cfg.Random
	if random == nil {
		random = rand.Reader
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Manager{
		transport:      cfg.Transport,
		observer:       cfg.Observer,
		generation:     cfg.Generation,
		limits:         limits,
		cleanupTimeout: cleanupTimeout,
		random:         random,
		now:            now,
	}, nil
}

func validateLimits(limits Limits) error {
	if limits.KeyHold < 0 || limits.InterKey < 0 || limits.DoubleClickDelay < 0 ||
		limits.MaxActions <= 0 || limits.MaxActions > MaxActions ||
		limits.MaxBatchDuration <= 0 || limits.MaxWaitDuration <= 0 || limits.MaxTotalWait <= 0 ||
		limits.MaxWaitDuration > limits.MaxTotalWait || limits.MaxTotalWait > limits.MaxBatchDuration ||
		limits.MaxObservationAge <= 0 {
		return errors.New("invalid input limits")
	}
	return nil
}

func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.uncertain {
		return StateUncertain
	}
	return StateReady
}

func (m *Manager) Generation() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.generation
}

func (m *Manager) Acquire(expectedGeneration uint64) (*Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.uncertain {
		return nil, ErrInputUncertain
	}
	if expectedGeneration != m.generation {
		return nil, ErrStaleGeneration
	}
	if m.leaseHeld {
		return nil, ErrLeaseBusy
	}
	var nonce [16]byte
	if _, err := io.ReadFull(m.random, nonce[:]); err != nil {
		return nil, fmt.Errorf("create lease token: %w", err)
	}
	m.active = nonce
	m.leaseHeld = true
	return &Lease{manager: m, token: GenerationToken{generation: m.generation, nonce: nonce}}, nil
}

// RunActions acquires a single-use exclusive lease, executes the prevalidated
// batch, and always attempts input neutralization before returning.
func (m *Manager) RunActions(ctx context.Context, expectedGeneration uint64, batch Batch) (BatchReceipt, error) {
	lease, err := m.Acquire(expectedGeneration)
	if err != nil {
		return BatchReceipt{}, err
	}
	return lease.RunActions(ctx, batch)
}

// Reconcile sends authoritative neutral state and clears an uncertain latch.
// It is the only path that may make an uncertain manager ready again.
func (m *Manager) Reconcile(ctx context.Context, expectedGeneration uint64) error {
	m.mu.Lock()
	if expectedGeneration != m.generation {
		m.mu.Unlock()
		return ErrStaleGeneration
	}
	if m.leaseHeld {
		m.mu.Unlock()
		return ErrLeaseBusy
	}
	m.mu.Unlock()

	if err := m.neutralize(ctx, expectedGeneration); err != nil {
		m.markUncertain()
		return err
	}
	m.mu.Lock()
	m.uncertain = false
	m.mu.Unlock()
	return nil
}

// AdvanceGeneration neutralizes the old session before publishing a newer
// generation. A failure leaves the old generation fenced as uncertain.
func (m *Manager) AdvanceGeneration(ctx context.Context, next uint64) error {
	m.mu.Lock()
	if m.leaseHeld {
		m.mu.Unlock()
		return ErrLeaseBusy
	}
	if next <= m.generation {
		m.mu.Unlock()
		return ErrStaleGeneration
	}
	old := m.generation
	m.mu.Unlock()
	if err := m.neutralize(ctx, old); err != nil {
		m.markUncertain()
		return err
	}
	m.mu.Lock()
	m.generation = next
	m.uncertain = false
	m.mu.Unlock()
	return nil
}

// Fence immediately rejects further sends from the current generation. It is
// used when the device actor observes a disconnect or session replacement.
// The uncertain latch remains set even if the lease later neutralizes cleanly;
// callers must reconnect and Reconcile before accepting more input.
func (m *Manager) Fence(expectedGeneration uint64) error {
	m.mu.Lock()
	if expectedGeneration != m.generation {
		m.mu.Unlock()
		return ErrStaleGeneration
	}
	m.uncertain = true
	cancel := m.activeCancel
	m.mu.Unlock()
	if cancel != nil {
		cancel(ErrInputUncertain)
	}
	return nil
}

func (m *Manager) markUncertain() {
	m.mu.Lock()
	m.uncertain = true
	m.mu.Unlock()
}

func (m *Manager) validateToken(token GenerationToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.uncertain {
		return ErrInputUncertain
	}
	if !m.leaseHeld || token.generation != m.generation || token.nonce != m.active {
		return ErrStaleGeneration
	}
	return nil
}

func (m *Manager) rememberPointer(point Point) {
	m.mu.Lock()
	m.lastPointer = point
	m.mu.Unlock()
}

func (m *Manager) beginRelease(token GenerationToken) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.leaseHeld || token.nonce != m.active {
		return false
	}
	clear(m.active[:])
	m.activeCancel = nil
	return true
}

func (m *Manager) setActiveCancel(token GenerationToken, cancel context.CancelCauseFunc) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.uncertain {
		return ErrInputUncertain
	}
	if !m.leaseHeld || token.generation != m.generation || token.nonce != m.active {
		return ErrStaleGeneration
	}
	m.activeCancel = cancel
	return nil
}

func (m *Manager) finishRelease() {
	m.mu.Lock()
	m.leaseHeld = false
	m.mu.Unlock()
}

func (m *Manager) neutralize(ctx context.Context, generation uint64) error {
	m.mu.Lock()
	point := m.lastPointer
	m.mu.Unlock()
	keyboard, _ := KeyboardReport(0)
	absolute, _ := PointerReport(point.X, point.Y, 0)
	relative, _ := RelativeMouseReport(0, 0, 0)
	var errs []error
	for _, frame := range [][]byte{keyboard, absolute, relative} {
		if err := m.transport.SendHID(ctx, generation, Reliable, frame); err != nil {
			errs = append(errs, err)
		}
	}
	if err := m.transport.Flush(ctx, generation); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("%w: %w", ErrNeutralization, err)
	}
	return nil
}

type Lease struct {
	mu       sync.Mutex
	manager  *Manager
	token    GenerationToken
	released bool
}

func (l *Lease) Token() GenerationToken { return l.token }

func (l *Lease) Release() error {
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return nil
	}
	l.released = true
	l.mu.Unlock()

	if !l.manager.beginRelease(l.token) {
		return ErrLeaseReleased
	}
	defer l.manager.finishRelease()
	cleanupCtx, cancel := context.WithTimeoutCause(context.Background(), l.manager.cleanupTimeout, ErrNeutralization)
	defer cancel()
	if err := l.manager.neutralize(cleanupCtx, l.token.generation); err != nil {
		l.manager.markUncertain()
		return err
	}
	return nil
}

func (l *Lease) ensureValid() error {
	l.mu.Lock()
	released := l.released
	l.mu.Unlock()
	if released {
		return ErrLeaseReleased
	}
	return l.manager.validateToken(l.token)
}

func (l *Lease) abandon() {
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return
	}
	l.released = true
	l.mu.Unlock()
	if l.manager.beginRelease(l.token) {
		l.manager.finishRelease()
	}
}
