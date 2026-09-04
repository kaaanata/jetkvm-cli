package cli

import (
	"context"
	"io"
	"strings"
	"testing"
	"uuid"

	"github.com/kaaanata/jetkvm-cli/internal/automation"
	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/input"
	"github.com/kaaanata/jetkvm-cli/internal/operation"
)

func TestInputCommandOwnsEphemeralControlLifecycle(t *testing.T) {
	service := newFakeAutomation()
	stdout, stderr, app := newControlTestApp(t, false, service, nil)
	code := app.Execute(t.Context(), []string{"input", "key", "lab", "ENTER"})
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if service.open.DeviceID != "device-1" || len(service.open.Capabilities) != 1 || service.open.Capabilities[0] != "input" {
		t.Fatalf("open request = %+v", service.open)
	}
	if service.actions.Ref.ID != "ctl_test" || service.actions.Ref.ExpectedGeneration != 7 || service.runCalls != 1 {
		t.Fatalf("action request = %+v", service.actions)
	}
	if !strings.Contains(stdout.String(), `"operation_id":`) {
		t.Fatalf("JSON output = %s", stdout.String())
	}
}

func TestInputKeyMapsExplicitFenceAndOperation(t *testing.T) {
	service := newFakeAutomation()
	stdout, stderr, app := newControlTestApp(t, false, service, nil)
	operationID := uuid.NewV7()
	code := app.Execute(t.Context(), []string{
		"input", "key", "lab", "ENTER", "--handle=ctl_test", "--generation=7", "--operation-id=" + operationID.String(),
	})
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	request := service.actions
	if request.DeviceID != "device-1" || request.Ref.ID != "ctl_test" || request.Ref.ExpectedGeneration != 7 || request.OperationID != operationID {
		t.Fatalf("run request = %+v", request)
	}
	if got := request.Batch.Actions; len(got) != 1 || got[0].Type != input.ActionKeypress || len(got[0].Keys) != 1 || got[0].Keys[0] != "ENTER" {
		t.Fatalf("actions = %+v", got)
	}
	if !strings.Contains(stdout.String(), `"operation_id": "`+operationID.String()+`"`) {
		t.Fatalf("JSON output = %s", stdout.String())
	}
}

func TestInputRunRejectsUnknownJSON(t *testing.T) {
	tests := []struct {
		name string
		json string
		kind string
	}{
		{name: "unknown member", json: `[{"type":"keypress","keys":["ENTER"],"command":"rm"}]`, kind: "invalid_argument"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newFakeAutomation()
			stdout, stderr, app := newControlTestApp(t, false, service, nil)
			code := app.Execute(t.Context(), []string{"input", "run", "lab", "--handle=ctl_test", "--generation=7", "--actions-json=" + test.json})
			if code == ExitOK || service.runCalls != 0 {
				t.Fatalf("exit code = %d, calls = %d", code, service.runCalls)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), `"kind": "`+test.kind+`"`) {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestDangerousActionsRequireActionTimeConfirmation(t *testing.T) {
	t.Run("non-TTY fails closed", func(t *testing.T) {
		service := newFakeAutomation()
		_, stderr, app := newControlTestApp(t, false, service, nil)
		code := app.Execute(t.Context(), []string{"power", "reset", "lab", "--handle=ctl_test", "--generation=7"})
		if code != ExitAuth || service.powerCalls != 0 || !strings.Contains(stderr.String(), `"kind": "confirmation_required"`) {
			t.Fatalf("exit=%d calls=%d stderr=%q", code, service.powerCalls, stderr.String())
		}
	})

	t.Run("TTY issuer binds proof to execution context", func(t *testing.T) {
		service := newFakeAutomation()
		issuer := &fakeConfirmationIssuer{}
		_, stderr, app := newControlTestApp(t, true, service, issuer)
		code := app.Execute(t.Context(), []string{"power", "hold", "lab", "--handle=ctl_test", "--generation=7", "--output=json"})
		if code != ExitOK {
			t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
		}
		if issuer.calls != 1 || !issuer.request.Interactive || issuer.request.Action != "power.hold" || !service.proofSeen {
			t.Fatalf("issuer=%+v proofSeen=%t", issuer, service.proofSeen)
		}
	})
}

func newControlTestApp(t *testing.T, terminal bool, automationService AutomationService, issuer ConfirmationIssuer) (*strings.Builder, *strings.Builder, *App) {
	t.Helper()
	stdout := new(strings.Builder)
	stderr := new(strings.Builder)
	app := New(Dependencies{
		Devices:       &fakeDeviceService{devices: []domain.Device{{ID: "device-1", Alias: "lab", Exposed: true}}},
		Automation:    automationService,
		Confirmations: issuer,
		Stdin:         strings.NewReader(""),
		Stdout:        stdout,
		Stderr:        stderr,
		IsTerminal:    func(io.Writer) bool { return terminal },
	})
	return stdout, stderr, app
}

type proofContextKey struct{}

type fakeConfirmationIssuer struct {
	calls   int
	request ConfirmationRequest
}

func (f *fakeConfirmationIssuer) Issue(ctx context.Context, request ConfirmationRequest) (context.Context, error) {
	f.calls++
	f.request = request
	return context.WithValue(ctx, proofContextKey{}, true), nil
}

type fakeAutomation struct {
	open       automation.OpenControlRequest
	actions    automation.RunActionsRequest
	power      automation.PowerActionRequest
	runCalls   int
	powerCalls int
	proofSeen  bool
}

func newFakeAutomation() *fakeAutomation { return &fakeAutomation{} }

func (f *fakeAutomation) PrepareOpenControl(automation.OpenControlRequest) (automation.ConfirmationPlan, error) {
	return automation.ConfirmationPlan{}, nil
}

func (f *fakeAutomation) OpenControl(_ context.Context, request automation.OpenControlRequest) (control.Handle, error) {
	f.open = request
	return control.Handle{ID: "ctl_test", DeviceID: request.DeviceID, Generation: 7, Ownership: control.OwnershipOwned, Capabilities: request.Capabilities, State: control.HandleReady}, nil
}

func (f *fakeAutomation) GetControl(context.Context, automation.ControlRequest) (control.Snapshot, error) {
	return control.Snapshot{Transport: control.TransportRunning, Session: control.SessionReady}, nil
}

func (f *fakeAutomation) CloseControl(_ context.Context, request automation.ControlRequest) (control.Handle, error) {
	return control.Handle{ID: request.Ref.ID, DeviceID: request.DeviceID, Generation: request.Ref.ExpectedGeneration, State: control.HandleClosed}, nil
}

func (f *fakeAutomation) RunActions(_ context.Context, request automation.RunActionsRequest) (automation.RunActionsResult, error) {
	f.runCalls++
	f.actions = request
	return automation.RunActionsResult{Operation: testReceipt(request.OperationID, request.DeviceID, request.Ref.ExpectedGeneration, domain.EffectInput, "run_actions")}, nil
}

func (f *fakeAutomation) PrepareRunActions(request automation.RunActionsRequest) (automation.ConfirmationPlan, error) {
	return automation.ConfirmationPlan{Required: batchConfirmation(request.Batch.Actions) == confirmationRequired}, nil
}

func (f *fakeAutomation) GetPowerState(_ context.Context, request automation.ControlRequest) (automation.PowerState, error) {
	return automation.PowerState{DeviceID: request.DeviceID, ActiveExtension: "atx-power"}, nil
}

func (f *fakeAutomation) PowerAction(ctx context.Context, request automation.PowerActionRequest) (operation.Receipt, error) {
	f.powerCalls++
	f.power = request
	f.proofSeen, _ = ctx.Value(proofContextKey{}).(bool)
	return testReceipt(request.OperationID, request.DeviceID, request.Ref.ExpectedGeneration, domain.EffectPower, "power."+string(request.Action)), nil
}

func (f *fakeAutomation) PreparePowerAction(request automation.PowerActionRequest) (automation.ConfirmationPlan, error) {
	return automation.ConfirmationPlan{Required: request.Action == automation.PowerReset || request.Action == automation.PowerHold}, nil
}

func testReceipt(id uuid.UUID, deviceID domain.DeviceID, generation uint64, effect domain.EffectClass, action string) operation.Receipt {
	return operation.Receipt{
		Request: operation.Request{ID: id, DeviceID: deviceID, ControlGeneration: generation, Effect: effect, Action: action},
		Stage:   operation.StageCompleted, Delivery: operation.DeliveryTransportAccepted,
		Verification:  operation.Verification{Status: operation.VerificationNotRequested},
		TerminalClaim: operation.TerminalClaimNotProven,
	}
}

var _ AutomationService = (*fakeAutomation)(nil)
var _ ConfirmationIssuer = (*fakeConfirmationIssuer)(nil)
