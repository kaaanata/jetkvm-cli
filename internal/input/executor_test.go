package input

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRunActionsExecutesOrderedBatchAndNeutralizes(t *testing.T) {
	transport := &fakeTransport{}
	manager := testManager(t, transport, nil)
	receipt, err := manager.RunActions(t.Context(), 7, boundBatch(
		Action{Type: ActionMove, X: 100, Y: 200},
		Action{Type: ActionClick, X: 100, Y: 200, Button: ButtonLeft},
		Action{Type: ActionTypeText, Text: "A!"},
		Action{Type: ActionScroll, DeltaY: -1},
	))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != BatchAccepted || !receipt.Neutralized || len(receipt.Actions) != 4 {
		t.Fatalf("receipt = %+v", receipt)
	}
	for _, action := range receipt.Actions {
		if action.Status != ActionAccepted {
			t.Fatalf("action receipt = %+v", action)
		}
	}
	calls := transport.snapshot()
	if len(calls) != 13 {
		t.Fatalf("transport calls = %d, want 13: %+v", len(calls), calls)
	}
	if calls[0].reliability != Motion || calls[1].reliability != Reliable {
		t.Fatalf("move/click reliability = %v/%v", calls[0].reliability, calls[1].reliability)
	}
	if calls[8].kind != "wheel" || calls[9].payload[0] != messageKeyboardReport || calls[11].payload[0] != messageMouseReport {
		t.Fatalf("terminal call sequence = %+v", calls[8:])
	}
}

func TestBatchIsFullyValidatedBeforeSending(t *testing.T) {
	transport := &fakeTransport{}
	manager := testManager(t, transport, nil)
	_, err := manager.RunActions(t.Context(), 7, boundBatch(
		Action{Type: ActionMove, X: 1, Y: 1},
		Action{Type: ActionTypeText, Text: "unsupported\n"},
	))
	if !errors.Is(err, ErrUnsupportedText) {
		t.Fatalf("RunActions error = %v", err)
	}
	if calls := transport.snapshot(); len(calls) != 0 {
		t.Fatalf("invalid batch sent %d transport calls", len(calls))
	}
	if _, err := manager.Acquire(7); err != nil {
		t.Fatalf("validation failure leaked lease: %v", err)
	}
}

func TestExclusiveLeaseAndGenerationFencing(t *testing.T) {
	transport := &fakeTransport{}
	manager := testManager(t, transport, nil)
	lease, err := manager.Acquire(7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Acquire(7); !errors.Is(err, ErrLeaseBusy) {
		t.Fatalf("second Acquire error = %v", err)
	}
	if err := manager.AdvanceGeneration(t.Context(), 8); !errors.Is(err, ErrLeaseBusy) {
		t.Fatalf("AdvanceGeneration while leased error = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if err := manager.AdvanceGeneration(t.Context(), 8); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Acquire(7); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale Acquire error = %v", err)
	}
	if _, err := lease.RunActions(t.Context(), Batch{Actions: []Action{{Type: ActionWait, Duration: time.Millisecond}}}); !errors.Is(err, ErrLeaseReleased) {
		t.Fatalf("released lease error = %v", err)
	}
}

func TestPartialExecutionAndAmbiguousSend(t *testing.T) {
	transport := &fakeTransport{failAt: 2}
	manager := testManager(t, transport, nil)
	receipt, err := manager.RunActions(t.Context(), 7, boundBatch(
		Action{Type: ActionMove, X: 1, Y: 2},
		Action{Type: ActionScroll, DeltaY: 1},
		Action{Type: ActionMove, X: 3, Y: 4},
	))
	if err == nil {
		t.Fatal("RunActions unexpectedly succeeded")
	}
	if receipt.Status != BatchAmbiguous || !receipt.Neutralized {
		t.Fatalf("receipt = %+v", receipt)
	}
	if receipt.Actions[0].Status != ActionAccepted || receipt.Actions[1].Status != ActionAmbiguous || receipt.Actions[2].Status != ActionNotStarted {
		t.Fatalf("action receipts = %+v", receipt.Actions)
	}
	if manager.State() != StateReady {
		t.Fatalf("manager state = %s; successful neutralization should restore safe input state", manager.State())
	}
}

func TestCancellationStopsRemainingActionsAndStillNeutralizes(t *testing.T) {
	firstSend := make(chan struct{})
	transport := &fakeTransport{onFirst: func() { close(firstSend) }}
	manager := testManager(t, transport, nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var receipt BatchReceipt
	var runErr error
	go func() {
		receipt, runErr = manager.RunActions(ctx, 7, boundBatch(
			Action{Type: ActionMove, X: 1, Y: 2},
			Action{Type: ActionWait, Duration: time.Second},
			Action{Type: ActionMove, X: 3, Y: 4},
		))
		close(done)
	}()
	<-firstSend
	cancel()
	<-done
	if !errors.Is(runErr, context.Canceled) || receipt.Status != BatchPartial || !receipt.Neutralized {
		t.Fatalf("receipt/error = %+v / %v", receipt, runErr)
	}
	if receipt.Actions[1].Status != ActionFailed || receipt.Actions[2].Status != ActionNotStarted {
		t.Fatalf("action receipts = %+v", receipt.Actions)
	}
}

func TestNeutralizationFailureLatchesUncertainUntilReconcile(t *testing.T) {
	transport := &fakeTransport{failFlush: true}
	manager := testManager(t, transport, nil)
	receipt, err := manager.RunActions(t.Context(), 7, boundBatch(Action{Type: ActionMove, X: 1, Y: 2}))
	if !errors.Is(err, ErrNeutralization) || receipt.Status != BatchAmbiguous || manager.State() != StateUncertain {
		t.Fatalf("receipt/error/state = %+v / %v / %s", receipt, err, manager.State())
	}
	if _, err := manager.Acquire(7); !errors.Is(err, ErrInputUncertain) {
		t.Fatalf("Acquire in uncertain state error = %v", err)
	}
	transport.mu.Lock()
	transport.failFlush = false
	transport.mu.Unlock()
	if err := manager.Reconcile(t.Context(), 7); err != nil {
		t.Fatal(err)
	}
	if manager.State() != StateReady {
		t.Fatalf("manager state = %s", manager.State())
	}
}

func TestDisconnectFencesRemainingActions(t *testing.T) {
	transport := &fakeTransport{}
	manager := testManager(t, transport, nil)
	transport.onFirst = func() {
		if err := manager.Fence(7); err != nil {
			panic(err)
		}
	}
	receipt, err := manager.RunActions(t.Context(), 7, boundBatch(
		Action{Type: ActionMove, X: 1, Y: 2},
		Action{Type: ActionMove, X: 3, Y: 4},
	))
	if !errors.Is(err, ErrInputUncertain) {
		t.Fatalf("RunActions error = %v", err)
	}
	if receipt.Status != BatchPartial || receipt.Actions[0].Status != ActionAccepted || receipt.Actions[1].Status != ActionFailed {
		t.Fatalf("receipt = %+v", receipt)
	}
	if !receipt.Neutralized || manager.State() != StateUncertain {
		t.Fatalf("neutralized/state = %v/%s", receipt.Neutralized, manager.State())
	}
}

func TestPanicStillNeutralizes(t *testing.T) {
	transport := &fakeTransport{}
	manager := testManager(t, transport, panicObserver{})
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()
		_, _ = manager.RunActions(t.Context(), 7, Batch{Actions: []Action{{Type: ActionScreenshot}}})
	}()
	calls := transport.snapshot()
	if len(calls) != 4 || calls[len(calls)-1].kind != "flush" {
		t.Fatalf("panic cleanup calls = %+v", calls)
	}
}

func TestScreenshotRequiresMatchingGeneration(t *testing.T) {
	manager := testManager(t, &fakeTransport{}, fixedObserver{observation: Observation{ID: "obs", Generation: 6}})
	receipt, err := manager.RunActions(t.Context(), 7, Batch{Actions: []Action{{Type: ActionScreenshot}}})
	if !errors.Is(err, ErrStaleGeneration) || receipt.Actions[0].Status != ActionFailed || !receipt.Neutralized {
		t.Fatalf("receipt/error = %+v / %v", receipt, err)
	}
}

type transportCall struct {
	kind        string
	generation  uint64
	reliability Reliability
	payload     []byte
	wheelY      int8
	wheelX      int8
}

type fakeTransport struct {
	mu        sync.Mutex
	calls     []transportCall
	callCount int
	failAt    int
	failFlush bool
	onFirst   func()
}

func (f *fakeTransport) SendHID(ctx context.Context, generation uint64, reliability Reliability, payload []byte) error {
	return f.record(ctx, transportCall{kind: "hid", generation: generation, reliability: reliability, payload: bytes.Clone(payload)})
}

func (f *fakeTransport) SendWheel(ctx context.Context, generation uint64, y, x int8) error {
	return f.record(ctx, transportCall{kind: "wheel", generation: generation, wheelY: y, wheelX: x})
}

func (f *fakeTransport) Flush(ctx context.Context, generation uint64) error {
	f.mu.Lock()
	f.calls = append(f.calls, transportCall{kind: "flush", generation: generation})
	fail := f.failFlush
	f.mu.Unlock()
	if fail {
		return errors.New("flush disconnected")
	}
	return ctx.Err()
}

func (f *fakeTransport) record(ctx context.Context, call transportCall) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	f.callCount++
	count := f.callCount
	f.calls = append(f.calls, call)
	onFirst := f.onFirst
	if count == 1 {
		f.onFirst = nil
	}
	fail := f.failAt == count
	f.mu.Unlock()
	if count == 1 && onFirst != nil {
		onFirst()
	}
	if fail {
		return errors.New("transport disconnected")
	}
	return nil
}

func (f *fakeTransport) snapshot() []transportCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]transportCall(nil), f.calls...)
}

func testManager(t *testing.T, transport HIDTransport, observer ScreenshotObserver) *Manager {
	t.Helper()
	manager, err := NewManager(ManagerConfig{
		Transport:  transport,
		Observer:   observer,
		Generation: 7,
		Limits: Limits{
			KeyHold:           0,
			InterKey:          0,
			DoubleClickDelay:  time.Millisecond,
			MaxActions:        MaxActions,
			MaxBatchDuration:  MaxBatchDuration,
			MaxWaitDuration:   MaxWaitDuration,
			MaxTotalWait:      MaxTotalWait,
			MaxObservationAge: 2 * time.Second,
		},
		CleanupTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func boundBatch(actions ...Action) Batch {
	return Batch{
		Observation: &ObservationBinding{
			ID: "obs-before", Generation: 7, Width: 1920, Height: 1080, CapturedAt: time.Now(),
		},
		Actions: actions,
	}
}

type fixedObserver struct{ observation Observation }

func (o fixedObserver) Capture(context.Context, uint64) (Observation, error) {
	return o.observation, nil
}

type panicObserver struct{}

func (panicObserver) Capture(context.Context, uint64) (Observation, error) { panic("observer panic") }

func TestKeyHoldSendsKeepalivesAndReleases(t *testing.T) {
	transport := &fakeTransport{}
	manager := testManager(t, transport, nil)
	receipt, err := manager.RunActions(t.Context(), 7, Batch{Actions: []Action{{Type: ActionKeyHold, Keys: []string{"SHIFT"}, Duration: 160 * time.Millisecond}}})
	if err != nil || !receipt.Neutralized || receipt.Status != BatchAccepted {
		t.Fatalf("hold: %+v %v", receipt, err)
	}
	keepalives := 0
	for _, call := range transport.snapshot() {
		if len(call.payload) == 1 && call.payload[0] == 0x09 {
			keepalives++
		}
	}
	if keepalives < 2 {
		t.Fatalf("keepalives=%d", keepalives)
	}
}

func TestKeyHoldCancellationStillNeutralizes(t *testing.T) {
	transport := &fakeTransport{}
	manager := testManager(t, transport, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer cancel()
	receipt, err := manager.RunActions(ctx, 7, Batch{Actions: []Action{{Type: ActionKeyHold, Keys: []string{"SHIFT"}, Duration: time.Second}}})
	if !errors.Is(err, context.DeadlineExceeded) || !receipt.Neutralized || receipt.Status != BatchAmbiguous {
		t.Fatalf("cancel hold: %+v %v", receipt, err)
	}
}

func TestKeyHoldBoundsRejectBeforeInput(t *testing.T) {
	for _, duration := range []time.Duration{0, -time.Second, MaxKeyHoldDuration + time.Millisecond} {
		transport := &fakeTransport{}
		manager := testManager(t, transport, nil)
		_, err := manager.RunActions(t.Context(), 7, Batch{Actions: []Action{{Type: ActionKeyHold, Keys: []string{"SHIFT"}, Duration: duration}}})
		if !errors.Is(err, ErrInvalidAction) || len(transport.snapshot()) != 0 {
			t.Fatalf("duration %s: %v", duration, err)
		}
	}
}
