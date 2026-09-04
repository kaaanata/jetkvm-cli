package control

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaaanata/jetkvm-cli/internal/domain"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

type fakeSession struct {
	closed        atomic.Int32
	disconnected  atomic.Int32
	closeFailures atomic.Int32
}

func (s *fakeSession) Close(context.Context) error {
	s.closed.Add(1)
	if s.closeFailures.CompareAndSwap(1, 0) {
		return errors.New("close failed")
	}
	return nil
}

func (s *fakeSession) Disconnect(context.Context) error {
	s.disconnected.Add(1)
	return nil
}

type openRecord struct {
	deviceID   domain.DeviceID
	generation uint64
}

type fakeFactory struct {
	mu             sync.Mutex
	records        []openRecord
	sessions       []*fakeSession
	failFirstClose bool
}

func (f *fakeFactory) Open(_ context.Context, deviceID domain.DeviceID, generation uint64, _ []string) (Session, error) {
	session := &fakeSession{}
	f.mu.Lock()
	if f.failFirstClose && len(f.sessions) == 0 {
		session.closeFailures.Store(1)
	}
	f.records = append(f.records, openRecord{deviceID: deviceID, generation: generation})
	f.sessions = append(f.sessions, session)
	f.mu.Unlock()
	return session, nil
}

type fakeLock struct {
	released  atomic.Int32
	onRelease func()
}

func (l *fakeLock) Release() error {
	if l.released.Add(1) == 1 {
		l.onRelease()
	}
	return nil
}

type fakeLocker struct {
	mu     sync.Mutex
	active map[domain.DeviceID]bool
	locks  []*fakeLock
}

func (l *fakeLocker) Acquire(_ context.Context, deviceID domain.DeviceID) (Lock, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active[deviceID] {
		return nil, errors.New("device already locked")
	}
	l.active[deviceID] = true
	lock := &fakeLock{onRelease: func() {
		l.mu.Lock()
		delete(l.active, deviceID)
		l.mu.Unlock()
	}}
	l.locks = append(l.locks, lock)
	return lock, nil
}

func newTestRegistry(t *testing.T) (*Registry, *fakeClock, *fakeFactory, *fakeLocker) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, time.September, 5, 1, 2, 3, 0, time.UTC)}
	factory := &fakeFactory{}
	locker := &fakeLocker{active: make(map[domain.DeviceID]bool)}
	var ids atomic.Int64
	registry, err := NewRegistry(Config{
		Factory:          factory,
		Locker:           locker,
		IdleTimeout:      DefaultIdleTimeout,
		AbsoluteLifetime: DefaultAbsoluteLifetime,
		SweepInterval:    time.Hour,
		CleanupTimeout:   time.Second,
		Now:              clock.Now,
		NewHandleID: func() HandleID {
			return HandleID("ctl-test-" + time.Unix(ids.Add(1), 0).Format("150405"))
		},
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := registry.Drain(ctx); err != nil {
			t.Errorf("Drain() cleanup error = %v", err)
		}
	})
	return registry, clock, factory, locker
}

func TestDefaultLifetimesAndLifecycleStates(t *testing.T) {
	registry, clock, _, _ := newTestRegistry(t)
	handle, err := registry.Open(t.Context(), OpenRequest{
		DeviceID:     "device-a",
		Capabilities: []string{"video", "input", "video"},
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if got := handle.IdleExpiresAt.Sub(clock.Now()); got != DefaultIdleTimeout {
		t.Fatalf("idle lifetime = %v, want %v", got, DefaultIdleTimeout)
	}
	if got := handle.AbsoluteExpiresAt.Sub(clock.Now()); got != DefaultAbsoluteLifetime {
		t.Fatalf("absolute lifetime = %v, want %v", got, DefaultAbsoluteLifetime)
	}
	if len(handle.Capabilities) != 2 || handle.Capabilities[0] != "input" || handle.Capabilities[1] != "video" {
		t.Fatalf("capabilities = %v, want normalized input/video", handle.Capabilities)
	}

	snapshot, err := registry.Get(t.Context(), handle.DeviceID, Ref{ID: handle.ID, ExpectedGeneration: handle.Generation})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if snapshot.Transport != TransportRunning || snapshot.Session != SessionReady || snapshot.Handle.State != HandleReady {
		t.Fatalf("snapshot = %+v, want running/ready/ready", snapshot)
	}
}

func TestSameDeviceWritesSerializeAndDifferentDevicesRunConcurrently(t *testing.T) {
	registry, _, _, _ := newTestRegistry(t)
	a := mustOpen(t, registry, "device-a")
	b := mustOpen(t, registry, "device-b")

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	otherStarted := make(chan struct{})
	errorsChannel := make(chan error, 3)

	go func() {
		errorsChannel <- registry.Execute(t.Context(), a.DeviceID, ref(a), "input", func(context.Context, Session) error {
			close(firstStarted)
			<-releaseFirst
			return nil
		})
	}()
	<-firstStarted
	go func() {
		errorsChannel <- registry.Execute(t.Context(), a.DeviceID, ref(a), "input", func(context.Context, Session) error {
			close(secondStarted)
			return nil
		})
	}()
	go func() {
		errorsChannel <- registry.Execute(t.Context(), b.DeviceID, ref(b), "input", func(context.Context, Session) error {
			close(otherStarted)
			return nil
		})
	}()

	select {
	case <-otherStarted:
	case <-time.After(time.Second):
		t.Fatal("different device write did not run concurrently")
	}
	select {
	case <-secondStarted:
		t.Fatal("same device write ran concurrently")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("serialized write did not start after predecessor")
	}
	for range 3 {
		if err := <-errorsChannel; err != nil {
			t.Errorf("Execute() error = %v", err)
		}
	}
}

func TestIdleAndAbsoluteExpiry(t *testing.T) {
	registry, clock, factory, locker := newTestRegistry(t)
	handle := mustOpen(t, registry, "device-a")
	clock.Advance(DefaultIdleTimeout + time.Nanosecond)
	_, err := registry.Get(t.Context(), handle.DeviceID, ref(handle))
	if !errors.Is(err, ErrControlExpired) {
		t.Fatalf("Get() error = %v, want ErrControlExpired", err)
	}
	if factory.sessions[0].closed.Load() != 1 || locker.locks[0].released.Load() != 1 {
		t.Fatal("idle expiry did not close session and release lock")
	}

	second := mustOpen(t, registry, "device-a")
	clock.Advance(DefaultIdleTimeout - time.Second)
	if _, err := registry.Get(t.Context(), second.DeviceID, ref(second)); err != nil {
		t.Fatalf("touch Get() error = %v", err)
	}
	clock.Advance(DefaultAbsoluteLifetime)
	_, err = registry.Get(t.Context(), second.DeviceID, ref(second))
	if !errors.Is(err, ErrControlExpired) {
		t.Fatalf("absolute Get() error = %v, want ErrControlExpired", err)
	}
}

func TestReconnectFencesOldHandleAndIncrementsGeneration(t *testing.T) {
	registry, _, factory, locker := newTestRegistry(t)
	first := mustOpen(t, registry, "device-a")
	second, err := registry.Reconnect(t.Context(), first.DeviceID, ref(first))
	if err != nil {
		t.Fatalf("Reconnect() error = %v", err)
	}
	if second.Generation != first.Generation+1 || second.ID == first.ID {
		t.Fatalf("reconnected handle = %+v, first = %+v", second, first)
	}
	if err := registry.Execute(t.Context(), first.DeviceID, ref(first), "input", func(context.Context, Session) error { return nil }); !errors.Is(err, ErrGenerationMismatch) {
		t.Fatalf("old Execute() error = %v, want ErrGenerationMismatch", err)
	}
	if err := registry.Execute(t.Context(), second.DeviceID, Ref{ID: second.ID, ExpectedGeneration: first.Generation}, "input", func(context.Context, Session) error { return nil }); !errors.Is(err, ErrGenerationMismatch) {
		t.Fatalf("stale generation error = %v, want ErrGenerationMismatch", err)
	}
	if factory.sessions[0].closed.Load() != 1 {
		t.Fatal("reconnect did not close replaced session")
	}
	if locker.locks[0].released.Load() != 0 {
		t.Fatal("reconnect released cross-process lock between generations")
	}
}

func TestCloseIsIdempotentAndAttachedDisconnects(t *testing.T) {
	registry, _, factory, locker := newTestRegistry(t)
	handle, err := registry.Open(t.Context(), OpenRequest{
		DeviceID:     "device-a",
		Capabilities: []string{"input"},
		Ownership:    OwnershipAttached,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	closed, err := registry.Close(t.Context(), handle.DeviceID, ref(handle))
	if err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	closedAgain, err := registry.Close(t.Context(), handle.DeviceID, ref(handle))
	if err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if closed.State != HandleClosed || closedAgain.State != HandleClosed {
		t.Fatalf("closed states = %q/%q", closed.State, closedAgain.State)
	}
	if factory.sessions[0].closed.Load() != 0 || factory.sessions[0].disconnected.Load() != 1 {
		t.Fatal("attached session must disconnect exactly once")
	}
	if locker.locks[0].released.Load() != 1 {
		t.Fatal("close did not release device lock exactly once")
	}
}

func TestDrainCancelsActiveWriteAndRejectsNewWork(t *testing.T) {
	registry, _, factory, locker := newTestRegistry(t)
	handle := mustOpen(t, registry, "device-a")
	started := make(chan struct{})
	executeResult := make(chan error, 1)
	go func() {
		executeResult <- registry.Execute(context.Background(), handle.DeviceID, ref(handle), "input", func(ctx context.Context, _ Session) error {
			close(started)
			<-ctx.Done()
			return context.Cause(ctx)
		})
	}()
	<-started

	drainCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := registry.Drain(drainCtx); err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if !errors.Is(<-executeResult, ErrControlBusy) {
		t.Fatal("active write was not cancelled by drain")
	}
	if registry.State() != TransportClosed {
		t.Fatalf("State() = %q, want closed", registry.State())
	}
	if _, err := registry.Open(t.Context(), OpenRequest{DeviceID: "device-b", Capabilities: []string{"input"}}); !errors.Is(err, ErrRegistryClosed) {
		t.Fatalf("Open() after drain error = %v, want ErrRegistryClosed", err)
	}
	if factory.sessions[0].closed.Load() != 1 || locker.locks[0].released.Load() != 1 {
		t.Fatal("shutdown did not close session and release lock")
	}
}

func TestExecuteRequiresGrantedCapability(t *testing.T) {
	registry, _, _, _ := newTestRegistry(t)
	handle := mustOpen(t, registry, "device-a")
	err := registry.Execute(t.Context(), handle.DeviceID, ref(handle), "power", func(context.Context, Session) error { return nil })
	if !errors.Is(err, ErrCapabilityMissing) {
		t.Fatalf("Execute() error = %v, want ErrCapabilityMissing", err)
	}
}

func TestFailedCloseRetainsLockUntilShutdownRetriesCleanup(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	factory := &fakeFactory{failFirstClose: true}
	locker := &fakeLocker{active: make(map[domain.DeviceID]bool)}
	registry, err := NewRegistry(Config{
		Factory: factory, Locker: locker, Now: clock.Now,
		SweepInterval: time.Hour, CleanupTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	handle := mustOpen(t, registry, "device-a")
	if _, err := registry.Close(t.Context(), handle.DeviceID, ref(handle)); err == nil {
		t.Fatal("Close() error = nil, want close failure")
	}
	if locker.locks[0].released.Load() != 0 {
		t.Fatal("uncertain close released the cross-process lock")
	}
	snapshot, err := registry.Get(t.Context(), handle.DeviceID, ref(handle))
	if !errors.Is(err, ErrControlBusy) || snapshot.Session != SessionUncertain {
		t.Fatalf("Get() = (%+v, %v), want uncertain busy control", snapshot, err)
	}
	if err := registry.Drain(t.Context()); err != nil {
		t.Fatalf("Drain() retry cleanup error = %v", err)
	}
	if factory.sessions[0].closed.Load() != 2 || locker.locks[0].released.Load() != 1 {
		t.Fatal("shutdown did not retry uncertain close before releasing lock")
	}
}

func TestSweeperClosesIdleSession(t *testing.T) {
	factory := &fakeFactory{}
	locker := &fakeLocker{active: make(map[domain.DeviceID]bool)}
	registry, err := NewRegistry(Config{
		Factory: factory, Locker: locker,
		IdleTimeout: 5 * time.Millisecond, AbsoluteLifetime: time.Second,
		SweepInterval: time.Millisecond, CleanupTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	t.Cleanup(func() { _ = registry.Drain(context.Background()) })
	handle := mustOpen(t, registry, "device-a")
	deadline := time.Now().Add(time.Second)
	for factory.sessions[0].closed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if factory.sessions[0].closed.Load() != 1 {
		t.Fatal("idle sweeper did not close session")
	}
	if _, err := registry.Get(t.Context(), handle.DeviceID, ref(handle)); !errors.Is(err, ErrControlExpired) {
		t.Fatalf("Get() error = %v, want ErrControlExpired", err)
	}
}

func mustOpen(t *testing.T, registry *Registry, deviceID domain.DeviceID) Handle {
	t.Helper()
	handle, err := registry.Open(t.Context(), OpenRequest{DeviceID: deviceID, Capabilities: []string{"input"}})
	if err != nil {
		t.Fatalf("Open(%q) error = %v", deviceID, err)
	}
	return handle
}

func ref(handle Handle) Ref {
	return Ref{ID: handle.ID, ExpectedGeneration: handle.Generation}
}
