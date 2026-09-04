// Package control owns per-device control-session lifecycles and serialization.
// It deliberately knows nothing about MCP, CLI, or the JetKVM wire protocol.
package control

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"
	"uuid"

	"github.com/kaaanata/jetkvm-cli/internal/domain"
)

const (
	DefaultIdleTimeout      = 5 * time.Minute
	DefaultAbsoluteLifetime = 30 * time.Minute
	DefaultSweepInterval    = time.Second
	DefaultCleanupTimeout   = 5 * time.Second
)

var (
	ErrInvalidConfig      = errors.New("invalid control configuration")
	ErrControlNotFound    = errors.New("control handle was not found")
	ErrControlExpired     = errors.New("control handle expired")
	ErrGenerationMismatch = errors.New("control generation mismatch")
	ErrControlBusy        = errors.New("control is draining")
	ErrRegistryClosed     = errors.New("control registry is closed")
	ErrCapabilityMissing  = errors.New("control capability is unavailable")
)

type TransportState string

const (
	TransportRunning  TransportState = "running"
	TransportDraining TransportState = "draining"
	TransportClosed   TransportState = "closed"
)

type SessionState string

const (
	SessionAbsent    SessionState = "absent"
	SessionOpening   SessionState = "opening"
	SessionReady     SessionState = "ready"
	SessionDraining  SessionState = "draining"
	SessionClosing   SessionState = "closing"
	SessionClosed    SessionState = "closed"
	SessionUncertain SessionState = "uncertain"
)

type HandleState string

const (
	HandleReady    HandleState = "ready"
	HandleDraining HandleState = "draining"
	HandleExpired  HandleState = "expired"
	HandleClosed   HandleState = "closed"
	HandleFenced   HandleState = "fenced"
)

type Ownership string

const (
	OwnershipOwned    Ownership = "owned"
	OwnershipAttached Ownership = "attached"
)

// HandleID is an opaque control lease identifier.
type HandleID string

// Handle identifies one generation of one device control session.
type Handle struct {
	ID                HandleID
	DeviceID          domain.DeviceID
	Generation        uint64
	Ownership         Ownership
	Capabilities      []string
	State             HandleState
	CreatedAt         time.Time
	LastUsedAt        time.Time
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
}

// Ref fences an operation to the exact handle generation observed by its caller.
type Ref struct {
	ID                 HandleID
	ExpectedGeneration uint64
}

type OpenRequest struct {
	DeviceID         domain.DeviceID
	Capabilities     []string
	Ownership        Ownership
	IdleTimeout      time.Duration
	AbsoluteLifetime time.Duration
}

// Session is the protocol adapter resource owned by a DeviceActor. Concrete
// implementations may expose richer APIs to Execute callbacks.
type Session interface {
	Close(context.Context) error
	Disconnect(context.Context) error
}

// SessionFactory creates a fresh protocol session for one fenced generation.
type SessionFactory interface {
	Open(context.Context, domain.DeviceID, uint64, []string) (Session, error)
}

// Lock is held for the complete lifetime of a device session.
type Lock interface {
	Release() error
}

// Locker prevents independent local processes from controlling one device at
// the same time.
type Locker interface {
	Acquire(context.Context, domain.DeviceID) (Lock, error)
}

// ExecuteFunc runs inside the per-device write queue. The Session value is
// valid only for the duration of the callback and must not be retained.
type ExecuteFunc func(context.Context, Session) error

type Config struct {
	Factory          SessionFactory
	Locker           Locker
	IdleTimeout      time.Duration
	AbsoluteLifetime time.Duration
	SweepInterval    time.Duration
	CleanupTimeout   time.Duration
	Now              func() time.Time
	NewHandleID      func() HandleID
}

// Snapshot reports the three independent lifecycle state machines.
type Snapshot struct {
	Transport TransportState
	Session   SessionState
	Handle    *Handle
}

// Registry owns one actor per stable device identity.
type Registry struct {
	config Config

	mu        sync.Mutex
	actors    map[domain.DeviceID]*actor
	state     atomic.Uint32
	stop      chan struct{}
	done      chan struct{}
	drainOnce sync.Once
	drainMu   sync.Mutex
	drainErr  error
}

const (
	registryRunning uint32 = iota
	registryDraining
	registryClosed
)

func NewRegistry(config Config) (*Registry, error) {
	if config.Factory == nil || config.Locker == nil {
		return nil, fmt.Errorf("%w: session factory and locker are required", ErrInvalidConfig)
	}
	if config.IdleTimeout == 0 {
		config.IdleTimeout = DefaultIdleTimeout
	}
	if config.AbsoluteLifetime == 0 {
		config.AbsoluteLifetime = DefaultAbsoluteLifetime
	}
	if config.SweepInterval == 0 {
		config.SweepInterval = DefaultSweepInterval
	}
	if config.CleanupTimeout == 0 {
		config.CleanupTimeout = DefaultCleanupTimeout
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.NewHandleID == nil {
		config.NewHandleID = func() HandleID { return HandleID("ctl_" + uuid.NewV7().String()) }
	}
	if config.IdleTimeout <= 0 || config.AbsoluteLifetime <= 0 || config.IdleTimeout > config.AbsoluteLifetime || config.SweepInterval <= 0 || config.CleanupTimeout <= 0 {
		return nil, fmt.Errorf("%w: durations must be positive and idle timeout must not exceed absolute lifetime", ErrInvalidConfig)
	}

	registry := &Registry{
		config: config,
		actors: make(map[domain.DeviceID]*actor),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go registry.sweep()
	return registry, nil
}

func (r *Registry) Open(ctx context.Context, request OpenRequest) (Handle, error) {
	if request.DeviceID == "" || len(request.Capabilities) == 0 {
		return Handle{}, fmt.Errorf("%w: device ID and capabilities are required", ErrInvalidConfig)
	}
	if r.state.Load() != registryRunning {
		return Handle{}, ErrRegistryClosed
	}
	request.Capabilities = normalizeCapabilities(request.Capabilities)
	if request.Capabilities[0] == "" {
		return Handle{}, fmt.Errorf("%w: capabilities must not be empty strings", ErrInvalidConfig)
	}
	if request.Ownership == "" {
		request.Ownership = OwnershipOwned
	}
	if request.Ownership != OwnershipOwned && request.Ownership != OwnershipAttached {
		return Handle{}, fmt.Errorf("%w: unsupported ownership %q", ErrInvalidConfig, request.Ownership)
	}
	if request.IdleTimeout == 0 {
		request.IdleTimeout = r.config.IdleTimeout
	}
	if request.AbsoluteLifetime == 0 {
		request.AbsoluteLifetime = r.config.AbsoluteLifetime
	}
	if request.IdleTimeout <= 0 || request.AbsoluteLifetime <= 0 || request.IdleTimeout > request.AbsoluteLifetime {
		return Handle{}, fmt.Errorf("%w: invalid handle lifetime", ErrInvalidConfig)
	}
	actor, err := r.actor(request.DeviceID)
	if err != nil {
		return Handle{}, err
	}
	return actor.open(ctx, request)
}

func (r *Registry) Get(ctx context.Context, deviceID domain.DeviceID, ref Ref) (Snapshot, error) {
	actor, ok := r.find(deviceID)
	if !ok {
		return Snapshot{Transport: r.transportState(), Session: SessionAbsent}, ErrControlNotFound
	}
	return actor.snapshot(ctx, ref, true)
}

// Execute serializes state-changing work for one device while allowing other
// device actors to progress independently.
func (r *Registry) Execute(ctx context.Context, deviceID domain.DeviceID, ref Ref, capability string, execute ExecuteFunc) error {
	if capability == "" || execute == nil {
		return fmt.Errorf("%w: capability and execute callback are required", ErrInvalidConfig)
	}
	if r.state.Load() != registryRunning {
		return ErrRegistryClosed
	}
	actor, ok := r.find(deviceID)
	if !ok {
		return ErrControlNotFound
	}
	return actor.execute(ctx, ref, capability, execute)
}

// Reconnect replaces the protocol session, fences the old handle, and returns
// a new handle with a strictly greater generation.
func (r *Registry) Reconnect(ctx context.Context, deviceID domain.DeviceID, ref Ref) (Handle, error) {
	if r.state.Load() != registryRunning {
		return Handle{}, ErrRegistryClosed
	}
	actor, ok := r.find(deviceID)
	if !ok {
		return Handle{}, ErrControlNotFound
	}
	return actor.reconnect(ctx, ref)
}

func (r *Registry) Close(ctx context.Context, deviceID domain.DeviceID, ref Ref) (Handle, error) {
	actor, ok := r.find(deviceID)
	if !ok {
		return Handle{}, ErrControlNotFound
	}
	return actor.close(ctx, ref, HandleClosed)
}

// Drain stops accepting new work, drains every per-device queue, closes owned
// sessions, and releases all cross-process locks.
func (r *Registry) Drain(ctx context.Context) error {
	r.drainOnce.Do(func() {
		r.state.Store(registryDraining)
		close(r.stop)
		go r.performDrain()
	})
	select {
	case <-r.done:
		r.drainMu.Lock()
		defer r.drainMu.Unlock()
		return r.drainErr
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (r *Registry) performDrain() {
	r.mu.Lock()
	actors := make([]*actor, 0, len(r.actors))
	for _, actor := range r.actors {
		actors = append(actors, actor)
		actor.beginDrain()
	}
	r.mu.Unlock()

	var wait sync.WaitGroup
	errorsByActor := make(chan error, len(actors))
	for _, actor := range actors {
		wait.Go(func() {
			if err := actor.shutdown(context.Background()); err != nil {
				errorsByActor <- err
			}
		})
	}
	wait.Wait()
	close(errorsByActor)
	var errs []error
	for err := range errorsByActor {
		errs = append(errs, err)
	}
	r.drainMu.Lock()
	r.drainErr = errors.Join(errs...)
	r.drainMu.Unlock()
	r.state.Store(registryClosed)
	close(r.done)
}

func (r *Registry) State() TransportState { return r.transportState() }

func (r *Registry) transportState() TransportState {
	switch r.state.Load() {
	case registryRunning:
		return TransportRunning
	case registryDraining:
		return TransportDraining
	default:
		return TransportClosed
	}
}

func (r *Registry) actor(deviceID domain.DeviceID) (*actor, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.Load() != registryRunning {
		return nil, ErrRegistryClosed
	}
	if existing := r.actors[deviceID]; existing != nil {
		return existing, nil
	}
	created := newActor(deviceID, r.config, r.transportState)
	r.actors[deviceID] = created
	return created, nil
}

func (r *Registry) find(deviceID domain.DeviceID) (*actor, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	actor, ok := r.actors[deviceID]
	return actor, ok
}

func (r *Registry) sweep() {
	ticker := time.NewTicker(r.config.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.mu.Lock()
			actors := make([]*actor, 0, len(r.actors))
			for _, actor := range r.actors {
				actors = append(actors, actor)
			}
			r.mu.Unlock()
			for _, actor := range actors {
				actor.expire(r.config.Now())
			}
		case <-r.stop:
			return
		}
	}
}

func normalizeCapabilities(capabilities []string) []string {
	capabilities = slices.Clone(capabilities)
	slices.Sort(capabilities)
	return slices.Compact(capabilities)
}

func cloneHandle(handle Handle) Handle {
	handle.Capabilities = slices.Clone(handle.Capabilities)
	return handle
}

func hasCapabilities(available, requested []string) bool {
	for _, capability := range requested {
		if !slices.Contains(available, capability) {
			return false
		}
	}
	return true
}
