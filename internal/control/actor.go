package control

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/kaaanata/jetkvm-cli/internal/domain"
)

type command struct {
	run  func(*actorState)
	stop bool
}

type actor struct {
	deviceID       domain.DeviceID
	config         Config
	transportState func() TransportState
	commands       chan command
	draining       atomic.Bool
	lifecycle      context.Context
	cancel         context.CancelCauseFunc
	done           chan struct{}
}

type actorState struct {
	sessionState SessionState
	session      Session
	lock         Lock
	generation   uint64
	current      *Handle
	history      map[HandleID]Handle
	idleTimeout  time.Duration
	cleanup      time.Duration
}

func newActor(deviceID domain.DeviceID, config Config, transportState func() TransportState) *actor {
	lifecycle, cancel := context.WithCancelCause(context.Background())
	actor := &actor{
		deviceID:       deviceID,
		config:         config,
		transportState: transportState,
		commands:       make(chan command),
		lifecycle:      lifecycle,
		cancel:         cancel,
		done:           make(chan struct{}),
	}
	go actor.run()
	return actor
}

func (a *actor) run() {
	state := actorState{
		sessionState: SessionAbsent,
		history:      make(map[HandleID]Handle),
		cleanup:      a.config.CleanupTimeout,
	}
	for command := range a.commands {
		command.run(&state)
		if command.stop {
			break
		}
	}
	close(a.done)
}

func (a *actor) submit(ctx context.Context, run func(*actorState)) error {
	return a.submitCommand(ctx, command{run: run})
}

func (a *actor) submitCommand(ctx context.Context, command command) error {
	select {
	case a.commands <- command:
		return nil
	case <-a.done:
		return ErrRegistryClosed
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

type valueResult[T any] struct {
	value T
	err   error
}

func await[T any](ctx context.Context, result <-chan valueResult[T]) (T, error) {
	select {
	case response := <-result:
		return response.value, response.err
	case <-ctx.Done():
		var zero T
		return zero, context.Cause(ctx)
	}
}

func (a *actor) open(ctx context.Context, request OpenRequest) (Handle, error) {
	if a.draining.Load() {
		return Handle{}, ErrControlBusy
	}
	result := make(chan valueResult[Handle], 1)
	if err := a.submit(ctx, func(state *actorState) {
		if err := context.Cause(ctx); err != nil {
			result <- valueResult[Handle]{err: err}
			return
		}
		if a.draining.Load() {
			result <- valueResult[Handle]{err: ErrControlBusy}
			return
		}
		now := a.config.Now()
		state.expireIfNeeded(now)
		if state.current != nil && state.current.State == HandleReady {
			if request.Ownership != state.current.Ownership || !hasCapabilities(state.current.Capabilities, request.Capabilities) {
				result <- valueResult[Handle]{err: ErrControlBusy}
				return
			}
			state.idleTimeout = min(state.idleTimeout, request.IdleTimeout)
			requestedAbsoluteExpiry := state.current.CreatedAt.Add(request.AbsoluteLifetime)
			if requestedAbsoluteExpiry.Before(state.current.AbsoluteExpiresAt) {
				state.current.AbsoluteExpiresAt = requestedAbsoluteExpiry
			}
			state.touch(now)
			result <- valueResult[Handle]{value: cloneHandle(*state.current)}
			return
		}

		handleID, err := a.newHandleID(state)
		if err != nil {
			result <- valueResult[Handle]{err: err}
			return
		}
		state.sessionState = SessionOpening
		lock, err := a.config.Locker.Acquire(ctx, a.deviceID)
		if err != nil {
			state.sessionState = SessionAbsent
			result <- valueResult[Handle]{err: fmt.Errorf("acquire device control lock: %w", err)}
			return
		}
		if lock == nil {
			state.sessionState = SessionAbsent
			result <- valueResult[Handle]{err: fmt.Errorf("%w: locker returned a nil lock", ErrInvalidConfig)}
			return
		}
		generation := state.generation + 1
		session, err := a.config.Factory.Open(ctx, a.deviceID, generation, request.Capabilities)
		if err != nil {
			state.sessionState = SessionAbsent
			result <- valueResult[Handle]{err: errors.Join(fmt.Errorf("open device session: %w", err), lock.Release())}
			return
		}
		if session == nil {
			state.sessionState = SessionAbsent
			result <- valueResult[Handle]{err: errors.Join(fmt.Errorf("%w: session factory returned a nil session", ErrInvalidConfig), lock.Release())}
			return
		}
		state.generation = generation
		state.lock = lock
		state.session = session
		state.sessionState = SessionReady
		handle := Handle{
			ID:                handleID,
			DeviceID:          a.deviceID,
			Generation:        generation,
			Ownership:         request.Ownership,
			Capabilities:      slicesClone(request.Capabilities),
			State:             HandleReady,
			CreatedAt:         now,
			LastUsedAt:        now,
			IdleExpiresAt:     now.Add(request.IdleTimeout),
			AbsoluteExpiresAt: now.Add(request.AbsoluteLifetime),
		}
		state.current = &handle
		state.idleTimeout = request.IdleTimeout
		result <- valueResult[Handle]{value: cloneHandle(handle)}
	}); err != nil {
		return Handle{}, err
	}
	return await(ctx, result)
}

func (a *actor) snapshot(ctx context.Context, ref Ref, touch bool) (Snapshot, error) {
	result := make(chan valueResult[Snapshot], 1)
	if err := a.submit(ctx, func(state *actorState) {
		now := a.config.Now()
		state.expireIfNeeded(now)
		handle, err := state.validate(ref)
		if err != nil {
			result <- valueResult[Snapshot]{value: Snapshot{Transport: a.transportState(), Session: state.sessionState}, err: err}
			return
		}
		if touch && handle.State == HandleReady {
			state.touch(now)
			handle = state.current
		}
		copy := cloneHandle(*handle)
		result <- valueResult[Snapshot]{value: Snapshot{Transport: a.transportState(), Session: state.sessionState, Handle: &copy}}
	}); err != nil {
		return Snapshot{}, err
	}
	return await(ctx, result)
}

func (a *actor) execute(ctx context.Context, ref Ref, capability string, execute ExecuteFunc) error {
	if a.draining.Load() {
		return ErrControlBusy
	}
	result := make(chan error, 1)
	if err := a.submit(ctx, func(state *actorState) {
		if err := context.Cause(ctx); err != nil {
			result <- err
			return
		}
		if a.draining.Load() {
			result <- ErrControlBusy
			return
		}
		now := a.config.Now()
		state.expireIfNeeded(now)
		if _, err := state.validate(ref); err != nil {
			result <- err
			return
		}
		if !hasCapabilities(state.current.Capabilities, []string{capability}) {
			result <- ErrCapabilityMissing
			return
		}
		if state.sessionState != SessionReady || state.session == nil {
			result <- ErrControlBusy
			return
		}
		executeCtx, cancel := context.WithCancelCause(ctx)
		stop := context.AfterFunc(a.lifecycle, func() { cancel(ErrControlBusy) })
		err := execute(executeCtx, state.session)
		stop()
		cancel(nil)
		state.touch(a.config.Now())
		result <- err
	}); err != nil {
		return err
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (a *actor) reconnect(ctx context.Context, ref Ref) (Handle, error) {
	if a.draining.Load() {
		return Handle{}, ErrControlBusy
	}
	result := make(chan valueResult[Handle], 1)
	if err := a.submit(ctx, func(state *actorState) {
		if err := context.Cause(ctx); err != nil {
			result <- valueResult[Handle]{err: err}
			return
		}
		old, err := state.validate(ref)
		if err != nil {
			result <- valueResult[Handle]{err: err}
			return
		}
		handleID, idErr := a.newHandleID(state)
		if idErr != nil {
			result <- valueResult[Handle]{err: idErr}
			return
		}
		old.State = HandleFenced
		idle := state.idleTimeout
		lifetime := old.AbsoluteExpiresAt.Sub(old.CreatedAt)
		state.history[old.ID] = cloneHandle(*old)
		state.current = nil
		state.sessionState = SessionClosing
		closeErr := state.session.Close(ctx)
		if closeErr != nil {
			state.sessionState = SessionUncertain
			state.current = old
			result <- valueResult[Handle]{err: fmt.Errorf("close replaced device session: %w", closeErr)}
			return
		}
		state.session = nil

		state.sessionState = SessionOpening
		generation := state.generation + 1
		session, err := a.config.Factory.Open(ctx, a.deviceID, generation, old.Capabilities)
		if err != nil {
			state.sessionState = SessionClosed
			result <- valueResult[Handle]{err: errors.Join(fmt.Errorf("reopen device session: %w", err), state.releaseLock())}
			return
		}
		if session == nil {
			state.sessionState = SessionClosed
			result <- valueResult[Handle]{err: errors.Join(fmt.Errorf("%w: session factory returned a nil session", ErrInvalidConfig), state.releaseLock())}
			return
		}
		state.generation = generation
		state.session = session
		state.sessionState = SessionReady
		now := a.config.Now()
		handle := Handle{
			ID:                handleID,
			DeviceID:          a.deviceID,
			Generation:        generation,
			Ownership:         old.Ownership,
			Capabilities:      slicesClone(old.Capabilities),
			State:             HandleReady,
			CreatedAt:         now,
			LastUsedAt:        now,
			IdleExpiresAt:     now.Add(idle),
			AbsoluteExpiresAt: now.Add(lifetime),
		}
		state.current = &handle
		state.idleTimeout = idle
		result <- valueResult[Handle]{value: cloneHandle(handle)}
	}); err != nil {
		return Handle{}, err
	}
	return await(ctx, result)
}

func (a *actor) close(ctx context.Context, ref Ref, terminal HandleState) (Handle, error) {
	result := make(chan valueResult[Handle], 1)
	if err := a.submit(ctx, func(state *actorState) {
		if err := context.Cause(ctx); err != nil {
			result <- valueResult[Handle]{err: err}
			return
		}
		if historical, ok := state.history[ref.ID]; ok {
			if historical.Generation != ref.ExpectedGeneration {
				result <- valueResult[Handle]{err: ErrGenerationMismatch}
				return
			}
			result <- valueResult[Handle]{value: cloneHandle(historical)}
			return
		}
		handle, err := state.validate(ref)
		if err != nil {
			result <- valueResult[Handle]{err: err}
			return
		}
		handle.State = HandleDraining
		state.sessionState = SessionDraining
		closed, err := state.closeSession(ctx, terminal)
		result <- valueResult[Handle]{value: closed, err: err}
	}); err != nil {
		return Handle{}, err
	}
	return await(ctx, result)
}

func (a *actor) expire(now time.Time) {
	ctx, cancel := context.WithTimeoutCause(context.Background(), a.config.CleanupTimeout, errors.New("control expiry cleanup timed out"))
	defer cancel()
	_ = a.submit(ctx, func(state *actorState) { state.expireIfNeeded(now) })
}

func (a *actor) shutdown(ctx context.Context) error {
	result := make(chan error, 1)
	if err := a.submitCommand(ctx, command{stop: true, run: func(state *actorState) {
		if state.current == nil {
			result <- nil
			return
		}
		state.current.State = HandleDraining
		state.sessionState = SessionDraining
		cleanupCtx, cancel := context.WithTimeoutCause(context.Background(), a.config.CleanupTimeout, errors.New("control shutdown cleanup timed out"))
		defer cancel()
		_, err := state.closeSession(cleanupCtx, HandleClosed)
		result <- err
	}}); err != nil {
		return err
	}
	select {
	case err := <-result:
		<-a.done
		return err
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (a *actor) beginDrain() {
	a.draining.Store(true)
	a.cancel(ErrControlBusy)
}

func (a *actor) newHandleID(state *actorState) (HandleID, error) {
	id := a.config.NewHandleID()
	if id == "" {
		return "", fmt.Errorf("%w: handle ID generator returned an empty ID", ErrInvalidConfig)
	}
	if state.current != nil && state.current.ID == id {
		return "", fmt.Errorf("%w: handle ID generator returned a duplicate ID", ErrInvalidConfig)
	}
	if _, exists := state.history[id]; exists {
		return "", fmt.Errorf("%w: handle ID generator returned a duplicate ID", ErrInvalidConfig)
	}
	return id, nil
}

func (state *actorState) validate(ref Ref) (*Handle, error) {
	if state.current == nil || state.current.ID != ref.ID {
		if historical, ok := state.history[ref.ID]; ok {
			if historical.Generation != ref.ExpectedGeneration {
				return nil, ErrGenerationMismatch
			}
			switch historical.State {
			case HandleExpired:
				return nil, ErrControlExpired
			case HandleFenced:
				return nil, ErrGenerationMismatch
			default:
				return nil, ErrControlNotFound
			}
		}
		return nil, ErrControlNotFound
	}
	if state.current.Generation != ref.ExpectedGeneration {
		return nil, ErrGenerationMismatch
	}
	if state.current.State == HandleExpired {
		return nil, ErrControlExpired
	}
	if state.current.State != HandleReady {
		return nil, ErrControlBusy
	}
	return state.current, nil
}

func (state *actorState) touch(now time.Time) {
	state.current.LastUsedAt = now
	idleExpiry := now.Add(state.idleTimeout)
	if idleExpiry.After(state.current.AbsoluteExpiresAt) {
		idleExpiry = state.current.AbsoluteExpiresAt
	}
	state.current.IdleExpiresAt = idleExpiry
}

func (state *actorState) expireIfNeeded(now time.Time) {
	for id, handle := range state.history {
		if !now.Before(handle.AbsoluteExpiresAt) {
			delete(state.history, id)
		}
	}
	if state.current == nil || state.current.State != HandleReady {
		return
	}
	if now.Before(state.current.IdleExpiresAt) && now.Before(state.current.AbsoluteExpiresAt) {
		return
	}
	state.current.State = HandleDraining
	state.sessionState = SessionDraining
	ctx, cancel := context.WithTimeout(context.Background(), state.cleanup)
	defer cancel()
	_, _ = state.closeSession(ctx, HandleExpired)
}

func (state *actorState) closeSession(ctx context.Context, terminal HandleState) (Handle, error) {
	handle := cloneHandle(*state.current)
	state.sessionState = SessionClosing
	if state.session != nil {
		var err error
		if handle.Ownership == OwnershipOwned {
			err = state.session.Close(ctx)
		} else {
			err = state.session.Disconnect(ctx)
		}
		if err != nil {
			state.sessionState = SessionUncertain
			return handle, err
		}
		state.session = nil
	}
	if err := state.releaseLock(); err != nil {
		state.sessionState = SessionUncertain
		return handle, err
	}
	handle.State = terminal
	state.history[handle.ID] = cloneHandle(handle)
	state.current = nil
	state.sessionState = SessionClosed
	return handle, nil
}

func (state *actorState) releaseLock() error {
	if state.lock == nil {
		return nil
	}
	err := state.lock.Release()
	if err == nil {
		state.lock = nil
	}
	return err
}

func slicesClone(values []string) []string {
	return append([]string(nil), values...)
}
