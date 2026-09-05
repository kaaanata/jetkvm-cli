package automation

import (
	"context"
	"encoding/json/v2"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/kaaanata/jetkvm-cli/internal/config"
	"github.com/kaaanata/jetkvm-cli/internal/confirmation"
	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/input"
	"github.com/kaaanata/jetkvm-cli/internal/inventory"
	"github.com/kaaanata/jetkvm-cli/internal/operation"
	"github.com/kaaanata/jetkvm-cli/internal/policy"
	"github.com/kaaanata/jetkvm-cli/internal/store"
)

const testDeviceID = domain.DeviceID("device-1")

func TestTakeoverConfirmationBindsSessionLifetimes(t *testing.T) {
	service, _, _ := newTestService(t, []string{"video", "input"}, "serial-console")
	cfg := policyTestConfig([]string{"video", "input"})
	device := cfg.Devices["lab"]
	device.Takeover.RequireConfirmation = true
	cfg.Devices["lab"] = device
	compiled, err := policy.Compile(cfg, inventory.Static())
	if err != nil {
		t.Fatal(err)
	}
	service.policy = compiled
	request := OpenControlRequest{DeviceID: testDeviceID, Capabilities: []string{"video"}, IdleTimeout: time.Minute, AbsoluteLifetime: 2 * time.Minute}
	plan, err := service.PrepareOpenControl(request)
	if err != nil || !plan.Required {
		t.Fatalf("prepare confirmation: %+v, %v", plan, err)
	}
	ctx := mintTestProof(t, service, plan.Binding)
	changed := request
	changed.IdleTimeout += time.Nanosecond
	if _, err := service.OpenControl(ctx, changed); err == nil {
		t.Fatal("changed idle timeout accepted")
	}
	changed = request
	changed.AbsoluteLifetime += time.Nanosecond
	if _, err := service.OpenControl(ctx, changed); err == nil {
		t.Fatal("changed absolute lifetime accepted")
	}
	if _, err := service.OpenControl(ctx, request); err != nil {
		t.Fatalf("confirmed open: %v", err)
	}
	if _, err := service.OpenControl(ctx, request); err == nil {
		t.Fatal("confirmation replay accepted")
	}
}

func TestWaitActionPersistsTerminalReceipt(t *testing.T) {
	service, session, _ := newTestService(t, []string{"video", "input"}, "serial-console")
	handle := openTestControl(t, service, []string{"input"})
	request := RunActionsRequest{DeviceID: testDeviceID, Ref: control.Ref{ID: handle.ID, ExpectedGeneration: handle.Generation}, OperationID: uuid.NewV7(), Batch: input.Batch{Actions: []input.Action{{Type: input.ActionKeypress, Keys: []string{"ESC"}}, {Type: input.ActionWait, Duration: time.Millisecond}}}}
	result, err := service.RunActions(t.Context(), request)
	if err != nil || result.Operation.Stage != operation.StageCompleted || !result.Batch.Neutralized {
		t.Fatalf("wait result: %+v, %v", result, err)
	}
	sends := session.sendCount()
	result, err = service.RunActions(t.Context(), request)
	if err != nil || !result.Existing || session.sendCount() != sends {
		t.Fatalf("deduplicated wait: %+v, %v", result, err)
	}
}

func TestReleaseInputPersistsOneTerminalReceipt(t *testing.T) {
	service, session, _ := newTestService(t, []string{"video", "input"}, "atx-power")
	handle := openTestControl(t, service, []string{"input"})
	request := ReleaseInputRequest{
		DeviceID:    testDeviceID,
		Ref:         control.Ref{ID: handle.ID, ExpectedGeneration: handle.Generation},
		OperationID: uuid.NewV7(),
	}
	receipt, err := service.ReleaseInput(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Stage != operation.StageCompleted || receipt.Delivery != operation.DeliveryTransportAccepted || receipt.Action != "release_input" {
		t.Fatalf("receipt = %+v", receipt)
	}
	firstSends := session.sendCount()
	repeated, err := service.ReleaseInput(t.Context(), request)
	if err != nil || repeated.ID != receipt.ID || session.sendCount() != firstSends {
		t.Fatalf("deduplicated receipt = %+v, sends = %d -> %d, error = %v", repeated, firstSends, session.sendCount(), err)
	}
}

func TestRunActionsPersistsTerminalReceiptAndDeduplicates(t *testing.T) {
	service, session, operations := newTestService(t, []string{"video", "input"}, "atx-power")
	handle := openTestControl(t, service, []string{"input"})
	operationID := uuid.NewV7()
	request := RunActionsRequest{
		DeviceID:    testDeviceID,
		Ref:         control.Ref{ID: handle.ID, ExpectedGeneration: handle.Generation},
		OperationID: operationID,
		Batch:       input.Batch{Actions: []input.Action{{Type: input.ActionKeypress, Keys: []string{"ENTER"}}}},
	}

	result, err := service.RunActions(t.Context(), request)
	if err != nil {
		t.Fatalf("RunActions() error = %v", err)
	}
	if result.Operation.Stage != operation.StageCompleted || result.Operation.Delivery != operation.DeliveryTransportAccepted || !result.Batch.Neutralized {
		t.Fatalf("result = %+v", result)
	}
	firstSends := session.sendCount()
	if firstSends == 0 {
		t.Fatal("input operation sent no HID reports")
	}

	repeated, err := service.RunActions(t.Context(), request)
	if err != nil {
		t.Fatalf("deduplicated RunActions() error = %v", err)
	}
	if !repeated.Existing || repeated.Operation.ID != operationID {
		t.Fatalf("deduplicated result = %+v", repeated)
	}
	if got := session.sendCount(); got != firstSends {
		t.Fatalf("deduplicated operation sent HID again: %d -> %d", firstSends, got)
	}
	stored, err := operations.Get(t.Context(), operationID)
	if err != nil || stored.Stage != operation.StageCompleted {
		t.Fatalf("stored receipt = %+v, error = %v", stored, err)
	}
}

func TestRunActionsResumesExistingNotSentReceipt(t *testing.T) {
	service, session, operations := newTestService(t, []string{"video", "input"}, "atx-power")
	handle := openTestControl(t, service, []string{"input"})
	id := uuid.NewV7()
	batch := input.Batch{Actions: []input.Action{{Type: input.ActionKeypress, Keys: []string{"ENTER"}}}}
	canonical, err := canonicalJSON(struct {
		SchemaVersion int              `json:"schema_version"`
		DeviceID      domain.DeviceID  `json:"device_id"`
		HandleID      control.HandleID `json:"control_handle"`
		Generation    uint64           `json:"generation"`
		Batch         input.Batch      `json:"batch"`
	}{1, testDeviceID, handle.ID, handle.Generation, batch})
	if err != nil {
		t.Fatal(err)
	}
	digester, _ := operation.NewDigester([]byte("0123456789abcdef0123456789abcdef"))
	request, err := digester.NewRequest(id, testDeviceID, handle.Generation, operation.EffectInput, "run_actions", service.policy.Revision(), canonical)
	if err != nil {
		t.Fatal(err)
	}
	if receipt, existing, err := operations.Begin(t.Context(), request); err != nil || existing || receipt.Stage != operation.StageNotSent {
		t.Fatalf("preexisting receipt = %+v, existing = %v, error = %v", receipt, existing, err)
	}

	result, err := service.RunActions(t.Context(), RunActionsRequest{
		DeviceID:    testDeviceID,
		Ref:         control.Ref{ID: handle.ID, ExpectedGeneration: handle.Generation},
		OperationID: id,
		Batch:       batch,
	})
	if err != nil {
		t.Fatalf("RunActions() error = %v", err)
	}
	if !result.Existing || result.Operation.Stage != operation.StageCompleted || session.sendCount() == 0 {
		t.Fatalf("resumed result = %+v, sends = %d", result, session.sendCount())
	}
}

func TestRunActionsValidationFailsBeforeSendBoundary(t *testing.T) {
	service, session, _ := newTestService(t, []string{"video", "input"}, "atx-power")
	handle := openTestControl(t, service, []string{"input"})
	result, err := service.RunActions(t.Context(), RunActionsRequest{
		DeviceID:    testDeviceID,
		Ref:         control.Ref{ID: handle.ID, ExpectedGeneration: handle.Generation},
		OperationID: uuid.NewV7(),
		Batch:       input.Batch{Actions: []input.Action{{Type: input.ActionTypeText, Text: "\n"}}},
	})
	if !errors.Is(err, input.ErrUnsupportedText) {
		t.Fatalf("RunActions() error = %v, want unsupported text", err)
	}
	if result.Operation.Stage != operation.StageFailed || result.Operation.Delivery != operation.DeliveryNotSent || !result.Operation.RetrySafe {
		t.Fatalf("receipt = %+v", result.Operation)
	}
	if got := session.sendCount(); got != 0 {
		t.Fatalf("invalid batch sent %d HID reports", got)
	}
}

func TestRunActionsTransportFailureIsAmbiguousAndNeverReplayed(t *testing.T) {
	service, session, _ := newTestService(t, []string{"video", "input"}, "atx-power")
	handle := openTestControl(t, service, []string{"input"})
	session.failHID = true
	request := RunActionsRequest{
		DeviceID:    testDeviceID,
		Ref:         control.Ref{ID: handle.ID, ExpectedGeneration: handle.Generation},
		OperationID: uuid.NewV7(),
		Batch:       input.Batch{Actions: []input.Action{{Type: input.ActionKeypress, Keys: []string{"ENTER"}}}},
	}
	result, err := service.RunActions(t.Context(), request)
	if err == nil {
		t.Fatal("RunActions() error = nil")
	}
	if result.Operation.Stage != operation.StageAmbiguous || result.Operation.RetrySafe {
		t.Fatalf("receipt = %+v", result.Operation)
	}
	firstSends := session.sendCount()
	if _, err := service.RunActions(t.Context(), request); err != nil {
		t.Fatalf("deduplicated ambiguous operation error = %v", err)
	}
	if got := session.sendCount(); got != firstSends {
		t.Fatalf("ambiguous operation replayed: %d -> %d", firstSends, got)
	}
}

func TestRunActionsAreSerializedByDeviceActor(t *testing.T) {
	service, session, _ := newTestService(t, []string{"video", "input"}, "atx-power")
	handle := openTestControl(t, service, []string{"input"})
	start := make(chan struct{})
	errs := make(chan error, 4)
	var wait sync.WaitGroup
	for range 4 {
		wait.Go(func() {
			<-start
			_, err := service.RunActions(t.Context(), RunActionsRequest{
				DeviceID:    testDeviceID,
				Ref:         control.Ref{ID: handle.ID, ExpectedGeneration: handle.Generation},
				OperationID: uuid.NewV7(),
				Batch:       input.Batch{Actions: []input.Action{{Type: input.ActionKeypress, Keys: []string{"ENTER"}}}},
			})
			errs <- err
		})
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent RunActions() error = %v", err)
		}
	}
	if session.maxActive != 1 {
		t.Fatalf("maximum concurrent sessions = %d, want 1", session.maxActive)
	}
}

func TestPolicyDenialPrecedesOperationAndDeviceExecution(t *testing.T) {
	service, session, operations := newTestService(t, []string{"video", "input"}, "atx-power")
	handle := openTestControl(t, service, []string{"input"})
	id := uuid.NewV7()
	_, err := service.RunActions(t.Context(), RunActionsRequest{
		DeviceID:    testDeviceID,
		Ref:         control.Ref{ID: handle.ID, ExpectedGeneration: handle.Generation},
		Scope:       policy.Scope{ToolsetsDeny: []string{"input"}},
		OperationID: id,
		Batch:       input.Batch{Actions: []input.Action{{Type: input.ActionKeypress, Keys: []string{"ENTER"}}}},
	})
	if !errors.Is(err, domain.ErrCapabilityUnavailable) {
		t.Fatalf("RunActions() error = %v, want capability unavailable", err)
	}
	if got := session.sendCount(); got != 0 {
		t.Fatalf("denied operation sent %d reports", got)
	}
	if _, err := operations.Get(t.Context(), id); !errors.Is(err, operation.ErrNotFound) {
		t.Fatalf("denied operation entered ledger: %v", err)
	}
}

func TestPowerCapabilityUsesActiveExtensionAuthority(t *testing.T) {
	service, session, operations := newTestService(t, []string{"video", "power"}, "serial-console")
	handle := openTestControl(t, service, []string{"power"})
	id := uuid.NewV7()
	_, err := service.PowerAction(t.Context(), PowerActionRequest{
		DeviceID:    testDeviceID,
		Ref:         control.Ref{ID: handle.ID, ExpectedGeneration: handle.Generation},
		OperationID: id,
		Action:      PowerReset,
	})
	if !errors.Is(err, domain.ErrCapabilityUnavailable) {
		t.Fatalf("PowerAction() error = %v, want capability unavailable", err)
	}
	if session.rpcCount("setATXPowerAction") != 0 {
		t.Fatal("serial fixture received an ATX write")
	}
	stored, err := operations.Get(t.Context(), id)
	if err != nil || stored.Stage != operation.StageFailed || stored.Delivery != operation.DeliveryNotSent {
		t.Fatalf("capability-denied power receipt = %+v, error = %v", stored, err)
	}
}

func TestPowerActionPersistsAcceptedTerminalReceipt(t *testing.T) {
	service, session, _ := newTestService(t, []string{"video", "power"}, "atx-power")
	handle := openTestControl(t, service, []string{"power"})
	request := PowerActionRequest{
		DeviceID:    testDeviceID,
		Ref:         control.Ref{ID: handle.ID, ExpectedGeneration: handle.Generation},
		OperationID: uuid.NewV7(),
		Action:      PowerPress,
	}
	receipt, err := service.PowerAction(confirmedPowerContext(t, service, request), request)
	if err != nil {
		t.Fatalf("PowerAction() error = %v", err)
	}
	if receipt.Stage != operation.StageCompleted || receipt.Delivery != operation.DeliveryTransportAccepted || receipt.TerminalClaim != operation.TerminalClaimNotProven {
		t.Fatalf("receipt = %+v", receipt)
	}
	if got := session.lastPowerAction(); got != "power-short" {
		t.Fatalf("wire power action = %q", got)
	}
}

func TestPowerTransportFailureIsAmbiguous(t *testing.T) {
	service, session, _ := newTestService(t, []string{"video", "power"}, "atx-power")
	handle := openTestControl(t, service, []string{"power"})
	session.rpcFailure = errors.New("response lost")
	request := PowerActionRequest{
		DeviceID:    testDeviceID,
		Ref:         control.Ref{ID: handle.ID, ExpectedGeneration: handle.Generation},
		OperationID: uuid.NewV7(),
		Action:      PowerReset,
	}
	receipt, err := service.PowerAction(confirmedPowerContext(t, service, request), request)
	if err == nil {
		t.Fatal("PowerAction() error = nil")
	}
	if receipt.Stage != operation.StageAmbiguous || receipt.Delivery != operation.DeliveryPossiblySent || receipt.RetrySafe {
		t.Fatalf("receipt = %+v", receipt)
	}
}

func TestPowerConfirmationIsRequiredAtSendBoundary(t *testing.T) {
	service, session, _ := newTestService(t, []string{"video", "power"}, "atx-power")
	handle := openTestControl(t, service, []string{"power"})
	request := PowerActionRequest{
		DeviceID: testDeviceID, Ref: control.Ref{ID: handle.ID, ExpectedGeneration: handle.Generation},
		OperationID: uuid.NewV7(), Action: PowerReset,
	}
	receipt, err := service.PowerAction(t.Context(), request)
	if !errors.Is(err, confirmation.ErrPrincipalRequired) || receipt.Stage != operation.StageFailed || receipt.Delivery != operation.DeliveryNotSent {
		t.Fatalf("PowerAction() receipt = %+v, error = %v", receipt, err)
	}
	if session.rpcCount("setATXPowerAction") != 0 {
		t.Fatal("unconfirmed power operation crossed the send boundary")
	}
}

func TestPowerConfirmationCannotBeReplayed(t *testing.T) {
	service, session, _ := newTestService(t, []string{"video", "power"}, "atx-power")
	handle := openTestControl(t, service, []string{"power"})
	request := PowerActionRequest{
		DeviceID: testDeviceID, Ref: control.Ref{ID: handle.ID, ExpectedGeneration: handle.Generation},
		OperationID: uuid.NewV7(), Action: PowerReset,
	}
	ctx := confirmedPowerContext(t, service, request)
	if _, err := service.PowerAction(ctx, request); err != nil {
		t.Fatal(err)
	}
	request.OperationID = uuid.NewV7()
	if _, err := service.PowerAction(ctx, request); !errors.Is(err, confirmation.ErrProofReplayed) {
		t.Fatalf("replayed PowerAction() error = %v", err)
	}
	if got := session.rpcCount("setATXPowerAction"); got != 1 {
		t.Fatalf("power send count = %d, want 1", got)
	}
}

func TestInputCommitRequiresBoundConfirmation(t *testing.T) {
	service, session, _ := newTestService(t, []string{"video", "input"}, "atx-power")
	handle := openTestControl(t, service, []string{"input"})
	request := RunActionsRequest{
		DeviceID: testDeviceID, Ref: control.Ref{ID: handle.ID, ExpectedGeneration: handle.Generation},
		OperationID: uuid.NewV7(), Batch: input.Batch{Actions: []input.Action{
			{Type: input.ActionTypeText, Text: "shutdown"},
			{Type: input.ActionKeypress, Keys: []string{"ENTER"}},
		}},
	}
	result, err := service.RunActions(t.Context(), request)
	if !errors.Is(err, confirmation.ErrPrincipalRequired) || result.Operation.Stage != operation.StageFailed {
		t.Fatalf("RunActions() result = %+v, error = %v", result, err)
	}
	if session.sendCount() != 0 {
		t.Fatal("unconfirmed input commit sent HID reports")
	}
	request.OperationID = uuid.NewV7()
	ctx := confirmedInputContext(t, service, request)
	result, err = service.RunActions(ctx, request)
	if err != nil || result.Operation.Stage != operation.StageCompleted {
		t.Fatalf("confirmed RunActions() result = %+v, error = %v", result, err)
	}
}

func TestOpenControlEnforcesTakeoverPolicy(t *testing.T) {
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	digester, _ := operation.NewDigester([]byte("0123456789abcdef0123456789abcdef"))
	cfg := policyTestConfig([]string{"video", "input"})
	device := cfg.Devices["lab"]
	device.Takeover.Allowed = false
	cfg.Devices["lab"] = device
	compiled, err := policy.Compile(cfg, inventory.Static())
	if err != nil {
		t.Fatal(err)
	}
	session := newFakeRuntimeSession(t, 1, "atx-power")
	registry, err := control.NewRegistry(control.Config{Factory: fakeSessionFactory{session}, Locker: fakeLocker{}, SweepInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Drain(context.Background())
	service, err := NewService(Config{Registry: registry, Policy: compiled, Operations: operation.NewService(database), Digester: digester})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenControl(t.Context(), OpenControlRequest{DeviceID: testDeviceID, Capabilities: []string{"input"}}); !errors.Is(err, domain.ErrTakeoverDisabled) {
		t.Fatalf("OpenControl() error = %v, want takeover disabled", err)
	}
	if session.sendCount() != 0 {
		t.Fatal("takeover-denied control reached the device")
	}
}

func TestCloseControlNeutralizesBeforeSessionClose(t *testing.T) {
	service, session, _ := newTestService(t, []string{"video", "input"}, "atx-power")
	handle := openTestControl(t, service, []string{"input"})
	closed, err := service.CloseControl(t.Context(), ControlRequest{
		DeviceID: testDeviceID,
		Ref:      control.Ref{ID: handle.ID, ExpectedGeneration: handle.Generation},
	})
	if err != nil {
		t.Fatalf("CloseControl() error = %v", err)
	}
	if closed.State != control.HandleClosed || !session.closed || session.sendCount() != 3 || session.flushes != 1 {
		t.Fatalf("closed = %+v, session = %+v", closed, session)
	}
}

func TestFileLockerExcludesSecondOwner(t *testing.T) {
	locker, err := NewFileLocker(filepath.Join(t.TempDir(), "locks"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := locker.Acquire(t.Context(), testDeviceID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := locker.Acquire(ctx, testDeviceID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Acquire() error = %v, want deadline", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
}

func openTestControl(t *testing.T, service *Service, capabilities []string) control.Handle {
	t.Helper()
	handle, err := service.OpenControl(t.Context(), OpenControlRequest{DeviceID: testDeviceID, Capabilities: capabilities})
	if err != nil {
		t.Fatalf("OpenControl() error = %v", err)
	}
	return handle
}

func confirmedPowerContext(t *testing.T, service *Service, request PowerActionRequest) context.Context {
	t.Helper()
	canonical, err := canonicalJSON(struct {
		SchemaVersion int              `json:"schema_version"`
		DeviceID      domain.DeviceID  `json:"device_id"`
		HandleID      control.HandleID `json:"control_handle"`
		Generation    uint64           `json:"generation"`
		Action        PowerAction      `json:"action"`
	}{1, request.DeviceID, request.Ref.ID, request.Ref.ExpectedGeneration, request.Action})
	if err != nil {
		t.Fatal(err)
	}
	binding := confirmation.Binding{
		DeviceID: request.DeviceID, Generation: request.Ref.ExpectedGeneration,
		Effect: domain.EffectPower, Action: "power." + string(request.Action),
		ArgumentsDigest: confirmation.DigestArguments(canonical), PolicyRevision: service.policy.Revision(),
	}
	return mintTestProof(t, service, binding)
}

func confirmedInputContext(t *testing.T, service *Service, request RunActionsRequest) context.Context {
	t.Helper()
	canonical, err := canonicalJSON(struct {
		SchemaVersion int              `json:"schema_version"`
		DeviceID      domain.DeviceID  `json:"device_id"`
		HandleID      control.HandleID `json:"control_handle"`
		Generation    uint64           `json:"generation"`
		Batch         input.Batch      `json:"batch"`
	}{1, request.DeviceID, request.Ref.ID, request.Ref.ExpectedGeneration, request.Batch})
	if err != nil {
		t.Fatal(err)
	}
	binding, required := service.inputConfirmationBinding(request, canonical)
	if !required {
		t.Fatal("test batch does not require input.commit confirmation")
	}
	return mintTestProof(t, service, binding)
}

func mintTestProof(t *testing.T, service *Service, binding confirmation.Binding) context.Context {
	t.Helper()
	authority, ok := service.confirmations.(*confirmation.Authority)
	if !ok {
		t.Fatal("test service does not use confirmation authority")
	}
	ctx := confirmation.WithPrincipal(t.Context(), confirmation.LocalProcessPrincipal())
	proof, err := authority.Mint(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	return confirmation.WithProof(ctx, proof)
}

func newTestService(t *testing.T, permissions []string, extension string) (*Service, *fakeRuntimeSession, *operation.Service) {
	t.Helper()
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	operations := operation.NewService(database)
	digester, err := operation.NewDigester([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := policy.Compile(policyTestConfig(permissions), inventory.Static())
	if err != nil {
		t.Fatal(err)
	}
	confirmations, err := confirmation.NewAuthority(confirmation.Config{
		Key: []byte("abcdef0123456789abcdef0123456789"), Nonces: confirmation.NewMemoryNonceStore(),
	})
	if err != nil {
		t.Fatal(err)
	}
	session := newFakeRuntimeSession(t, 1, extension)
	registry, err := control.NewRegistry(control.Config{
		Factory:       fakeSessionFactory{session: session},
		Locker:        fakeLocker{},
		SweepInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = registry.Drain(ctx)
	})
	service, err := NewService(Config{Registry: registry, Policy: compiled, Operations: operations, Digester: digester, Confirmations: confirmations})
	if err != nil {
		t.Fatal(err)
	}
	return service, session, operations
}

func policyTestConfig(permissions []string) config.Config {
	cfg := config.Default()
	cfg.Confirmation.Required = true
	cfg.State.Path = "/state.db"
	cfg.Toolsets.Allow = []string{"video", "input", "power"}
	cfg.Devices["lab"] = config.DeviceConfig{
		DeviceID: "device-1", Origin: "http://device.test", Exposed: true, AllowPlainHTTP: true,
		Credentials: config.CredentialConfig{Provider: config.CredentialNoPassword},
		Permissions: permissions,
		Takeover:    config.TakeoverConfig{Allowed: true},
		Session: config.SessionConfig{
			IdleTimeout:      config.Duration{Duration: 5 * time.Minute},
			AbsoluteLifetime: config.Duration{Duration: 30 * time.Minute},
		},
	}
	return cfg
}

type fakeSessionFactory struct{ session control.Session }

func (f fakeSessionFactory) Open(context.Context, domain.DeviceID, uint64, []string) (control.Session, error) {
	return f.session, nil
}

type fakeLock struct{}

func (fakeLock) Release() error { return nil }

type fakeLocker struct{}

func (fakeLocker) Acquire(context.Context, domain.DeviceID) (control.Lock, error) {
	return fakeLock{}, nil
}

type fakeRuntimeSession struct {
	mu          sync.Mutex
	generation  uint64
	manager     *input.Manager
	extension   string
	failHID     bool
	sends       int
	flushes     int
	closed      bool
	rpcCalls    map[string]int
	powerAction string
	barrier     *sendBarrier
	active      int
	maxActive   int
	rpcFailure  error
	observe     func(context.Context) (ScreenObservation, error)
}

func (s *fakeRuntimeSession) Observe(ctx context.Context, _ time.Duration, _ time.Time) (ScreenObservation, error) {
	if s.observe == nil {
		return ScreenObservation{}, domain.ErrCapabilityUnavailable
	}
	return s.observe(ctx)
}

func newFakeRuntimeSession(t *testing.T, generation uint64, extension string) *fakeRuntimeSession {
	t.Helper()
	session := &fakeRuntimeSession{generation: generation, extension: extension, rpcCalls: make(map[string]int)}
	manager, err := input.NewManager(input.ManagerConfig{Transport: session, Generation: generation})
	if err != nil {
		t.Fatal(err)
	}
	session.manager = manager
	return session
}

func (s *fakeRuntimeSession) RunActions(ctx context.Context, batch input.Batch, start func(context.Context) error) (input.BatchReceipt, bool, error) {
	s.mu.Lock()
	s.active++
	s.maxActive = max(s.maxActive, s.active)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	s.barrier = &sendBarrier{start: start}
	receipt, err := s.manager.RunActions(ctx, s.generation, batch)
	started := s.barrier.started.Load()
	s.barrier = nil
	return receipt, started, err
}

func (s *fakeRuntimeSession) ReleaseInput(ctx context.Context, start func(context.Context) error) (bool, error) {
	s.barrier = &sendBarrier{start: start}
	err := s.manager.Reconcile(ctx, s.generation)
	started := s.barrier.started.Load()
	s.barrier = nil
	return started, err
}

func (s *fakeRuntimeSession) SendHID(ctx context.Context, generation uint64, _ input.Reliability, _ []byte) error {
	if generation != s.generation {
		return input.ErrStaleGeneration
	}
	if s.barrier != nil {
		if err := s.barrier.cross(ctx); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends++
	if s.failHID {
		return errors.New("HID delivery failed")
	}
	return nil
}

func (s *fakeRuntimeSession) SendWheel(ctx context.Context, generation uint64, _, _ int8) error {
	return s.SendHID(ctx, generation, input.Reliable, nil)
}

func (s *fakeRuntimeSession) Flush(context.Context, uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushes++
	return nil
}

func (s *fakeRuntimeSession) CallRPC(_ context.Context, method string, params, result any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rpcCalls[method]++
	switch method {
	case "getActiveExtension":
		*result.(*string) = s.extension
	case "getATXState":
		encoded, _ := json.Marshal(struct {
			Power bool `json:"power"`
			HDD   bool `json:"hdd"`
		}{Power: true})
		_ = json.Unmarshal(encoded, result)
	case "setATXPowerAction":
		encoded, _ := json.Marshal(params)
		var value struct {
			Action string `json:"action"`
		}
		_ = json.Unmarshal(encoded, &value)
		s.powerAction = value.Action
		if s.rpcFailure != nil {
			return s.rpcFailure
		}
	}
	return nil
}

func (s *fakeRuntimeSession) Close(context.Context) error {
	if err := s.manager.Reconcile(context.Background(), s.generation); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *fakeRuntimeSession) Disconnect(ctx context.Context) error { return s.Close(ctx) }

func (s *fakeRuntimeSession) sendCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sends
}

func (s *fakeRuntimeSession) rpcCount(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rpcCalls[method]
}

func (s *fakeRuntimeSession) lastPowerAction() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.powerAction
}

var (
	_ runtimeSession     = (*fakeRuntimeSession)(nil)
	_ input.HIDTransport = (*fakeRuntimeSession)(nil)
)

func TestInputConfirmationUsesCompiledKeyIdentity(t *testing.T) {
	for _, key := range []string{"ctrl-left", "CONTROLLEFT", "SUPER", "SUPERLEFT", "COMMANDRIGHT", "meta_right"} {
		t.Run(key, func(t *testing.T) {
			if !requiresInputCommit(input.Batch{Actions: []input.Action{{Type: input.ActionKeypress, Keys: []string{key, "A"}}}}) {
				t.Fatal("modifier alias bypassed confirmation")
			}
		})
	}
	for _, key := range []string{"F-1", "numpad_enter"} {
		t.Run(key, func(t *testing.T) {
			if !requiresInputCommit(input.Batch{Actions: []input.Action{{Type: input.ActionTypeText, Text: "test"}, {Type: input.ActionKeypress, Keys: []string{key}}}}) {
				t.Fatal("commit key alias bypassed confirmation")
			}
		})
	}
	if requiresInputCommit(input.Batch{Actions: []input.Action{{Type: input.ActionKeypress, Keys: []string{"SHIFT", "A"}}}}) {
		t.Fatal("ordinary shifted key unexpectedly requires confirmation")
	}
}

func TestSecondaryConfirmationDisabledPreservesExecutionBoundaries(t *testing.T) {
	service, session, _ := newTestService(t, []string{"video", "input", "power"}, "atx-power")
	cfg := policyTestConfig([]string{"video", "input", "power"})
	cfg.Confirmation.Required = false
	device := cfg.Devices["lab"]
	device.Takeover.RequireConfirmation = true
	cfg.Devices["lab"] = device
	compiled, err := policy.Compile(cfg, inventory.Static())
	if err != nil {
		t.Fatal(err)
	}
	service.policy = compiled
	// No verifier is needed when the policy does not require a secondary approval.
	service.confirmations = nil
	open := OpenControlRequest{DeviceID: testDeviceID, Capabilities: []string{"input", "video", "power"}}
	plan, err := service.PrepareOpenControl(open)
	if err != nil || plan.Required {
		t.Fatalf("open plan = %+v, %v", plan, err)
	}
	handle, err := service.OpenControl(t.Context(), open)
	if err != nil {
		t.Fatal(err)
	}
	ref := control.Ref{ID: handle.ID, ExpectedGeneration: handle.Generation}
	request := RunActionsRequest{DeviceID: testDeviceID, Ref: ref, OperationID: uuid.NewV7(), Batch: input.Batch{Actions: []input.Action{
		{Type: input.ActionKeypress, Keys: []string{"COMMAND", "SPACE"}},
	}}}
	plan, err = service.PrepareRunActions(request)
	if err != nil || plan.Required {
		t.Fatalf("input plan = %+v, %v", plan, err)
	}
	result, err := service.RunActions(t.Context(), request)
	if err != nil || result.Operation.Stage != operation.StageCompleted || !result.Batch.Neutralized {
		t.Fatalf("input result = %+v, %v", result, err)
	}
	sent := session.sendCount()
	request.Ref.ExpectedGeneration++
	request.OperationID = uuid.NewV7()
	if _, err := service.RunActions(t.Context(), request); err == nil {
		t.Fatal("stale generation accepted")
	}
	if session.sendCount() != sent {
		t.Fatal("stale generation sent input")
	}
	power := PowerActionRequest{DeviceID: testDeviceID, Ref: ref, OperationID: uuid.NewV7(), Action: PowerReset}
	plan, err = service.PreparePowerAction(power)
	if err != nil || plan.Required {
		t.Fatalf("power plan = %+v, %v", plan, err)
	}
	receipt, err := service.PowerAction(t.Context(), power)
	if err != nil || receipt.Stage != operation.StageCompleted || receipt.RetrySafe {
		t.Fatalf("power receipt = %+v, %v", receipt, err)
	}
	if session.rpcCount("setATXPowerAction") != 1 {
		t.Fatal("power action was not sent exactly once")
	}
	if _, err := service.CloseControl(t.Context(), ControlRequest{DeviceID: testDeviceID, Ref: ref}); err != nil {
		t.Fatal(err)
	}
	device.Takeover.Allowed = false
	cfg.Devices["lab"] = device
	service.policy, err = policy.Compile(cfg, inventory.Static())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenControl(t.Context(), open); !errors.Is(err, domain.ErrTakeoverDisabled) {
		t.Fatalf("takeover denial = %v", err)
	}
}
