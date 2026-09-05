// Package automation is the single application service for JetKVM control,
// bounded input, power operations, policy enforcement, and durable receipts.
package automation

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/kaaanata/jetkvm-cli/internal/confirmation"
	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/input"
	"github.com/kaaanata/jetkvm-cli/internal/jetkvm"
	"github.com/kaaanata/jetkvm-cli/internal/operation"
	"github.com/kaaanata/jetkvm-cli/internal/policy"
)

var (
	ErrInvalidConfiguration         = errors.New("invalid automation service configuration")
	ErrInvalidPowerAction           = errors.New("invalid power action")
	ErrTakeoverConfirmationRequired = errors.New("takeover confirmation is required")
)

type ConfirmationVerifier interface {
	VerifyAndConsume(context.Context, confirmation.Binding) error
}

type Config struct {
	Clients          map[domain.DeviceID]*jetkvm.Client
	SessionConfig    jetkvm.SessionConfig
	Policy           *policy.Compiled
	Operations       *operation.Service
	Digester         operation.Digester
	LockDirectory    string
	Confirmations    ConfirmationVerifier
	IdleTimeout      time.Duration
	AbsoluteLifetime time.Duration
	CleanupTimeout   time.Duration
	Registry         *control.Registry
	Now              func() time.Time
}

type Service struct {
	registry      *control.Registry
	policy        *policy.Compiled
	operations    *operation.Service
	digester      operation.Digester
	confirmations ConfirmationVerifier
	now           func() time.Time
}

const receiptFinalizeTimeout = 5 * time.Second

func NewService(cfg Config) (*Service, error) {
	if cfg.Policy == nil || cfg.Operations == nil {
		return nil, fmt.Errorf("%w: policy and operation service are required", ErrInvalidConfiguration)
	}
	registry := cfg.Registry
	if registry == nil {
		factory, err := NewSessionFactory(cfg.Clients, cfg.SessionConfig)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidConfiguration, err)
		}
		locker, err := NewFileLocker(cfg.LockDirectory)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidConfiguration, err)
		}
		registry, err = control.NewRegistry(control.Config{
			Factory:          factory,
			Locker:           locker,
			IdleTimeout:      cfg.IdleTimeout,
			AbsoluteLifetime: cfg.AbsoluteLifetime,
			CleanupTimeout:   cfg.CleanupTimeout,
			Now:              cfg.Now,
		})
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidConfiguration, err)
		}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		registry:      registry,
		policy:        cfg.Policy,
		operations:    cfg.Operations,
		digester:      cfg.Digester,
		confirmations: cfg.Confirmations,
		now:           now,
	}, nil
}

func (s *Service) OpenControl(ctx context.Context, request OpenControlRequest) (control.Handle, error) {
	if request.DeviceID == "" || len(request.Capabilities) == 0 {
		return control.Handle{}, errors.New("device ID and at least one capability are required")
	}
	capabilities := slices.Clone(request.Capabilities)
	slices.Sort(capabilities)
	capabilities = slices.Compact(capabilities)
	baseDecision, err := s.authorize("jetkvm_open_control", request.DeviceID, request.Scope, nil, false)
	if err != nil {
		return control.Handle{}, err
	}
	devicePolicy := baseDecision.Device
	for _, capability := range capabilities {
		tool, ok := controlCapabilityTool(capability)
		if !ok {
			return control.Handle{}, fmt.Errorf("unknown control capability %q", capability)
		}
		_, err := s.authorize(tool, request.DeviceID, request.Scope, nil, false)
		if err != nil {
			return control.Handle{}, err
		}
	}
	if !devicePolicy.TakeoverAllowed {
		return control.Handle{}, domain.ErrTakeoverDisabled
	}
	if devicePolicy.TakeoverRequiresConfirmation {
		if s.confirmations == nil {
			return control.Handle{}, ErrTakeoverConfirmationRequired
		}
		binding, err := s.openControlConfirmationBinding(request, capabilities)
		if err != nil {
			return control.Handle{}, err
		}
		if err := s.confirmations.VerifyAndConsume(ctx, binding); err != nil {
			return control.Handle{}, err
		}
	}
	if request.Ownership == control.OwnershipAttached {
		return control.Handle{}, errors.New("attached JetKVM sessions are not supported")
	}
	return s.registry.Open(ctx, control.OpenRequest{
		DeviceID:         request.DeviceID,
		Capabilities:     capabilities,
		Ownership:        control.OwnershipOwned,
		IdleTimeout:      request.IdleTimeout,
		AbsoluteLifetime: request.AbsoluteLifetime,
	})
}

// PrepareOpenControl returns the exact confirmation binding that OpenControl
// will verify. The application adapter must call this after applying device
// session defaults and before presenting any confirmation UI.
func (s *Service) PrepareOpenControl(request OpenControlRequest) (ConfirmationPlan, error) {
	if request.DeviceID == "" || len(request.Capabilities) == 0 {
		return ConfirmationPlan{}, errors.New("device ID and at least one capability are required")
	}
	capabilities := slices.Clone(request.Capabilities)
	slices.Sort(capabilities)
	capabilities = slices.Compact(capabilities)
	decision, err := s.authorize("jetkvm_open_control", request.DeviceID, request.Scope, nil, false)
	if err != nil {
		return ConfirmationPlan{}, err
	}
	if !decision.Device.TakeoverRequiresConfirmation {
		return ConfirmationPlan{}, nil
	}
	binding, err := s.openControlConfirmationBinding(request, capabilities)
	return ConfirmationPlan{Required: err == nil, Binding: binding}, err
}

func (s *Service) GetControl(ctx context.Context, request ControlRequest) (control.Snapshot, error) {
	if _, err := s.authorize("jetkvm_get_control", request.DeviceID, request.Scope, nil, false); err != nil {
		return control.Snapshot{}, err
	}
	return s.registry.Get(ctx, request.DeviceID, request.Ref)
}

func (s *Service) CloseControl(ctx context.Context, request ControlRequest) (control.Handle, error) {
	if _, err := s.authorize("jetkvm_close_control", request.DeviceID, request.Scope, nil, false); err != nil {
		return control.Handle{}, err
	}
	return s.registry.Close(ctx, request.DeviceID, request.Ref)
}

func (s *Service) RunActions(ctx context.Context, request RunActionsRequest) (RunActionsResult, error) {
	if _, err := s.authorize("jetkvm_run_actions", request.DeviceID, request.Scope, nil, false); err != nil {
		return RunActionsResult{}, err
	}
	if err := validateAutomationBatch(request.Batch); err != nil {
		return RunActionsResult{}, err
	}
	operationID := request.OperationID
	if operationID == uuid.Nil() {
		operationID = uuid.NewV7()
	}
	canonical, err := canonicalRunActions(request)
	if err != nil {
		return RunActionsResult{}, err
	}
	opRequest, err := s.digester.NewRequest(operationID, request.DeviceID, request.Ref.ExpectedGeneration, operation.EffectInput, "run_actions", s.policy.Revision(), canonical)
	if err != nil {
		return RunActionsResult{}, err
	}
	receipt, existing, err := s.operations.Begin(ctx, opRequest)
	if err != nil || (existing && receipt.Stage != operation.StageNotSent) {
		return RunActionsResult{Operation: receipt, Existing: existing}, err
	}
	snapshot, snapshotErr := s.registry.Get(ctx, request.DeviceID, request.Ref)
	if snapshotErr != nil {
		finalizeCtx, cancel := context.WithTimeoutCause(context.Background(), receiptFinalizeTimeout, context.DeadlineExceeded)
		defer cancel()
		terminal, terminalErr := s.finishNotSent(finalizeCtx, operationID, snapshotErr)
		return RunActionsResult{Operation: terminal}, errors.Join(snapshotErr, terminalErr)
	}

	var batchReceipt input.BatchReceipt
	var observation *ScreenObservation
	var observationErr error
	started := false
	confirmationBinding, confirmationRequired := s.inputConfirmationBinding(request, canonical)
	executeErr := s.registry.Execute(ctx, request.DeviceID, request.Ref, "input", func(executeCtx context.Context, session control.Session) error {
		adapter, err := automationSession(session)
		if err != nil {
			return err
		}
		if request.ObserveAfter || batchHasScreenshot(request.Batch) {
			if _, err := s.authorize("jetkvm_observe", request.DeviceID, request.Scope, nil, false); err != nil {
				return err
			}
			if snapshot.Handle == nil || !slices.Contains(snapshot.Handle.Capabilities, "video") {
				return domain.ErrCapabilityUnavailable
			}
			if _, ok := session.(screenSession); !ok {
				return domain.ErrCapabilityUnavailable
			}
		}
		if confirmationRequired {
			if s.confirmations == nil {
				return confirmation.ErrProofRequired
			}
			if verifyErr := s.confirmations.VerifyAndConsume(ctx, confirmationBinding); verifyErr != nil {
				return verifyErr
			}
		}
		batchReceipt, started, err = adapter.RunActions(executeCtx, request.Batch, func(sendCtx context.Context) error {
			_, markErr := s.operations.MarkSendStarted(sendCtx, operationID)
			return markErr
		})
		if err == nil && (request.ObserveAfter || batchHasScreenshot(request.Batch)) {
			var screen ScreenObservation
			if captured, ok := session.(interface{ lastCapture() *ScreenObservation }); ok && batchHasScreenshot(request.Batch) {
				observation = captured.lastCapture()
			} else {
				screen, observationErr = session.(screenSession).Observe(executeCtx, 0)
				if observationErr == nil {
					observation = &screen
				}
			}
		}
		return err
	})
	if executeErr != nil && started && batchReceipt.Status == input.BatchAccepted && batchReceipt.Neutralized {
		// Cancellation during optional observation cannot rewrite an already
		// accepted and neutralized input batch as ambiguous delivery.
		observationErr = errors.Join(observationErr, executeErr)
		executeErr = nil
	}
	if executeErr != nil {
		finalizeCtx, cancel := context.WithTimeoutCause(context.Background(), receiptFinalizeTimeout, context.DeadlineExceeded)
		defer cancel()
		if existing && !started {
			current, getErr := s.operations.Get(finalizeCtx, operationID)
			if getErr == nil && current.Stage != operation.StageNotSent {
				return RunActionsResult{Operation: current, Batch: batchReceipt, Existing: true}, nil
			}
		}
		terminal, terminalErr := s.finishActionFailure(finalizeCtx, operationID, started, batchReceipt, executeErr)
		return RunActionsResult{Operation: terminal, Batch: batchReceipt, Existing: existing}, errors.Join(executeErr, terminalErr)
	}
	if !started {
		cause := errors.New("action batch completed without crossing the device send boundary")
		finalizeCtx, cancel := context.WithTimeoutCause(context.Background(), receiptFinalizeTimeout, context.DeadlineExceeded)
		defer cancel()
		terminal, terminalErr := s.finishNotSent(finalizeCtx, operationID, cause)
		return RunActionsResult{Operation: terminal, Batch: batchReceipt}, errors.Join(cause, terminalErr)
	}
	finalizeCtx, cancel := context.WithTimeoutCause(context.Background(), receiptFinalizeTimeout, context.DeadlineExceeded)
	defer cancel()
	accepted, err := s.operations.Transition(finalizeCtx, operationID, operation.StageTransportAccepted, acceptedPatch())
	if err != nil {
		return RunActionsResult{Operation: accepted, Batch: batchReceipt, Existing: existing}, err
	}
	completed, err := s.operations.Transition(finalizeCtx, operationID, operation.StageCompleted, acceptedPatch())
	return RunActionsResult{Operation: completed, Batch: batchReceipt, Existing: existing, Observation: observation}, errors.Join(err, observationErr)
}

// PrepareRunActions returns the exact input.commit binding, when the batch
// crosses the configured confirmation threshold.
func (s *Service) PrepareRunActions(request RunActionsRequest) (ConfirmationPlan, error) {
	if _, err := s.authorize("jetkvm_run_actions", request.DeviceID, request.Scope, nil, false); err != nil {
		return ConfirmationPlan{}, err
	}
	if err := validateAutomationBatch(request.Batch); err != nil {
		return ConfirmationPlan{}, err
	}
	canonical, err := canonicalRunActions(request)
	if err != nil {
		return ConfirmationPlan{}, err
	}
	binding, required := s.inputConfirmationBinding(request, canonical)
	return ConfirmationPlan{Required: required, Binding: binding}, nil
}

// ReleaseInput sends the authoritative neutral keyboard and pointer reports
// through the existing generation-fenced control session.
func (s *Service) ReleaseInput(ctx context.Context, request ReleaseInputRequest) (operation.Receipt, error) {
	if _, err := s.authorize("jetkvm_release_input", request.DeviceID, request.Scope, nil, false); err != nil {
		return operation.Receipt{}, err
	}
	operationID := request.OperationID
	if operationID == uuid.Nil() {
		operationID = uuid.NewV7()
	}
	canonical, err := canonicalJSON(struct {
		SchemaVersion int              `json:"schema_version"`
		DeviceID      domain.DeviceID  `json:"device_id"`
		HandleID      control.HandleID `json:"control_handle"`
		Generation    uint64           `json:"generation"`
	}{1, request.DeviceID, request.Ref.ID, request.Ref.ExpectedGeneration})
	if err != nil {
		return operation.Receipt{}, err
	}
	opRequest, err := s.digester.NewRequest(operationID, request.DeviceID, request.Ref.ExpectedGeneration, operation.EffectInput, "release_input", s.policy.Revision(), canonical)
	if err != nil {
		return operation.Receipt{}, err
	}
	receipt, existing, err := s.operations.Begin(ctx, opRequest)
	if err != nil || (existing && receipt.Stage != operation.StageNotSent) {
		return receipt, err
	}
	if _, err := s.registry.Get(ctx, request.DeviceID, request.Ref); err != nil {
		return s.failNewOperation(operationID, err)
	}
	started := false
	executeErr := s.registry.Execute(ctx, request.DeviceID, request.Ref, "input", func(executeCtx context.Context, session control.Session) error {
		releaser, ok := session.(inputReleaseSession)
		if !ok {
			return fmt.Errorf("%w: %T", ErrUnexpectedSession, session)
		}
		started, err = releaser.ReleaseInput(executeCtx, func(sendCtx context.Context) error {
			_, markErr := s.operations.MarkSendStarted(sendCtx, operationID)
			return markErr
		})
		return err
	})
	if executeErr != nil {
		finalizeCtx, cancel := context.WithTimeoutCause(context.Background(), receiptFinalizeTimeout, context.DeadlineExceeded)
		defer cancel()
		if started {
			terminal, terminalErr := s.operations.Transition(finalizeCtx, operationID, operation.StageAmbiguous, operation.Patch{
				Delivery: operation.DeliveryPossiblySent, Verification: operation.Verification{Status: operation.VerificationNotRequested},
				TerminalClaim: operation.TerminalClaimNotProven, RetrySafe: false, ErrorKind: errorKind(executeErr),
			})
			return terminal, errors.Join(executeErr, terminalErr)
		}
		terminal, terminalErr := s.finishNotSent(finalizeCtx, operationID, executeErr)
		return terminal, errors.Join(executeErr, terminalErr)
	}
	if !started {
		return s.failNewOperation(operationID, errors.New("input release completed without crossing the device send boundary"))
	}
	finalizeCtx, cancel := context.WithTimeoutCause(context.Background(), receiptFinalizeTimeout, context.DeadlineExceeded)
	defer cancel()
	if _, err := s.operations.Transition(finalizeCtx, operationID, operation.StageTransportAccepted, acceptedPatch()); err != nil {
		return operation.Receipt{}, err
	}
	return s.operations.Transition(finalizeCtx, operationID, operation.StageCompleted, operation.Patch{
		Delivery: operation.DeliveryTransportAccepted, Verification: operation.Verification{Status: operation.VerificationNotRequested},
		TerminalClaim: operation.TerminalClaimNotProven, RetrySafe: false,
	})
}

func (s *Service) GetPowerState(ctx context.Context, request ControlRequest) (PowerState, error) {
	if _, err := s.authorize("jetkvm_get_power_state", request.DeviceID, request.Scope, nil, false); err != nil {
		return PowerState{}, err
	}
	var state PowerState
	err := s.registry.Execute(ctx, request.DeviceID, request.Ref, "power", func(executeCtx context.Context, session control.Session) error {
		adapter, err := automationSession(session)
		if err != nil {
			return err
		}
		return readPowerState(executeCtx, adapter, request.DeviceID, s.now(), &state)
	})
	return state, err
}

func (s *Service) PowerAction(ctx context.Context, request PowerActionRequest) (operation.Receipt, error) {
	if _, err := s.authorize("jetkvm_power_action", request.DeviceID, request.Scope, nil, false); err != nil {
		return operation.Receipt{}, err
	}
	wireAction, ok := powerWireAction(request.Action)
	if !ok {
		return operation.Receipt{}, ErrInvalidPowerAction
	}
	operationID := request.OperationID
	if operationID == uuid.Nil() {
		operationID = uuid.NewV7()
	}
	canonical, err := canonicalPowerAction(request)
	if err != nil {
		return operation.Receipt{}, err
	}
	opRequest, err := s.digester.NewRequest(operationID, request.DeviceID, request.Ref.ExpectedGeneration, operation.EffectPower, "power."+string(request.Action), s.policy.Revision(), canonical)
	if err != nil {
		return operation.Receipt{}, err
	}
	receipt, existing, err := s.operations.Begin(ctx, opRequest)
	if err != nil || (existing && receipt.Stage != operation.StageNotSent) {
		return receipt, err
	}
	if _, err := s.registry.Get(ctx, request.DeviceID, request.Ref); err != nil {
		return s.failNewOperation(operationID, err)
	}
	if _, err := s.probePowerState(ctx, ControlRequest{DeviceID: request.DeviceID, Ref: request.Ref}); err != nil {
		return s.failNewOperation(operationID, err)
	}

	started := false
	executeErr := s.registry.Execute(ctx, request.DeviceID, request.Ref, "power", func(executeCtx context.Context, session control.Session) error {
		adapter, err := automationSession(session)
		if err != nil {
			return err
		}
		if powerActionRequiresConfirmation(request.Action) {
			binding := powerConfirmationBinding(request, canonical, s.policy.Revision())
			if s.confirmations == nil {
				return confirmation.ErrProofRequired
			}
			if err := s.confirmations.VerifyAndConsume(ctx, binding); err != nil {
				return err
			}
		}
		if _, err := s.operations.MarkSendStarted(executeCtx, operationID); err != nil {
			return err
		}
		started = true
		return adapter.CallRPC(executeCtx, "setATXPowerAction", struct {
			Action string `json:"action"`
		}{Action: wireAction}, nil)
	})
	if executeErr != nil {
		finalizeCtx, cancel := context.WithTimeoutCause(context.Background(), receiptFinalizeTimeout, context.DeadlineExceeded)
		defer cancel()
		if existing && !started {
			current, getErr := s.operations.Get(finalizeCtx, operationID)
			if getErr == nil && current.Stage != operation.StageNotSent {
				return current, nil
			}
		}
		terminal, terminalErr := s.finishPowerFailure(finalizeCtx, operationID, started, executeErr)
		return terminal, errors.Join(executeErr, terminalErr)
	}
	finalizeCtx, cancel := context.WithTimeoutCause(context.Background(), receiptFinalizeTimeout, context.DeadlineExceeded)
	defer cancel()
	accepted, err := s.operations.Transition(finalizeCtx, operationID, operation.StageTransportAccepted, acceptedPatch())
	if err != nil {
		return accepted, err
	}
	return s.operations.Transition(finalizeCtx, operationID, operation.StageCompleted, acceptedPatch())
}

// PreparePowerAction returns a proof requirement only for reset and hold.
// A short press remains policy-gated and non-retryable, but is not MRTR by
// default as specified by the public contract.
func (s *Service) PreparePowerAction(request PowerActionRequest) (ConfirmationPlan, error) {
	if _, err := s.authorize("jetkvm_power_action", request.DeviceID, request.Scope, nil, false); err != nil {
		return ConfirmationPlan{}, err
	}
	if _, ok := powerWireAction(request.Action); !ok {
		return ConfirmationPlan{}, ErrInvalidPowerAction
	}
	if !powerActionRequiresConfirmation(request.Action) {
		return ConfirmationPlan{}, nil
	}
	canonical, err := canonicalPowerAction(request)
	if err != nil {
		return ConfirmationPlan{}, err
	}
	return ConfirmationPlan{Required: true, Binding: powerConfirmationBinding(request, canonical, s.policy.Revision())}, nil
}

func (s *Service) failNewOperation(operationID uuid.UUID, cause error) (operation.Receipt, error) {
	finalizeCtx, cancel := context.WithTimeoutCause(context.Background(), receiptFinalizeTimeout, context.DeadlineExceeded)
	defer cancel()
	terminal, terminalErr := s.finishNotSent(finalizeCtx, operationID, cause)
	return terminal, errors.Join(cause, terminalErr)
}

func (s *Service) probePowerState(ctx context.Context, request ControlRequest) (PowerState, error) {
	var state PowerState
	err := s.registry.Execute(ctx, request.DeviceID, request.Ref, "power", func(executeCtx context.Context, session control.Session) error {
		adapter, err := automationSession(session)
		if err != nil {
			return err
		}
		return readPowerState(executeCtx, adapter, request.DeviceID, s.now(), &state)
	})
	return state, err
}

func (s *Service) Drain(ctx context.Context) error {
	return s.registry.Drain(ctx)
}

func (s *Service) authorize(tool string, deviceID domain.DeviceID, scope policy.Scope, capabilities map[string]bool, checkCapabilities bool) (policy.Decision, error) {
	decision := s.policy.Evaluate(policy.Evaluation{
		ToolName: tool, DeviceID: string(deviceID), Scope: scope,
		Capabilities: capabilities, CheckCapabilities: checkCapabilities,
	})
	if !decision.Allowed {
		return decision, fmt.Errorf("%w: %s", domain.ErrCapabilityUnavailable, decision.Reason)
	}
	return decision, nil
}

// Generation zero is reserved for confirmation of a takeover that creates the
// first concrete control generation. The resulting handle generation is never
// zero and must be used by all subsequent proofs.
const preControlGeneration uint64 = 0

func (s *Service) openControlConfirmationBinding(request OpenControlRequest, capabilities []string) (confirmation.Binding, error) {
	canonical, err := canonicalJSON(struct {
		SchemaVersion    int               `json:"schema_version"`
		DeviceID         domain.DeviceID   `json:"device_id"`
		Generation       uint64            `json:"generation"`
		Capabilities     []string          `json:"capabilities"`
		Ownership        control.Ownership `json:"ownership"`
		IdleTimeout      time.Duration     `json:"idle_timeout"`
		AbsoluteLifetime time.Duration     `json:"absolute_lifetime"`
	}{1, request.DeviceID, preControlGeneration, capabilities, control.OwnershipOwned, request.IdleTimeout, request.AbsoluteLifetime})
	if err != nil {
		return confirmation.Binding{}, err
	}
	return confirmation.Binding{
		DeviceID: request.DeviceID, Generation: preControlGeneration,
		Effect: domain.EffectObserve, Action: "control.takeover",
		ArgumentsDigest: confirmation.DigestArguments(canonical), PolicyRevision: s.policy.Revision(),
	}, nil
}

func (s *Service) inputConfirmationBinding(request RunActionsRequest, canonical []byte) (confirmation.Binding, bool) {
	if !requiresInputCommit(request.Batch) {
		return confirmation.Binding{}, false
	}
	return confirmation.Binding{
		DeviceID: request.DeviceID, Generation: request.Ref.ExpectedGeneration,
		Effect: domain.EffectInput, Action: "input.commit",
		ArgumentsDigest: confirmation.DigestArguments(canonical), PolicyRevision: s.policy.Revision(),
	}, true
}

func requiresInputCommit(batch input.Batch) bool {
	hasText := false
	hasCommitKey := false
	for _, action := range batch.Actions {
		switch action.Type {
		case input.ActionTypeText:
			hasText = true
			if utf8.RuneCountInString(action.Text) > 256 {
				return true
			}
		case input.ActionKeypress:
			if sensitiveChord(action.Keys) {
				return true
			}
			_, usages, err := input.CompileKeyCombo(action.Keys)
			if err == nil {
				for _, usage := range usages {
					if usage == 0x28 || usage == 0x58 || (usage >= 0x3a && usage <= 0x45) || (usage >= 0x68 && usage <= 0x73) {
						hasCommitKey = true
					}
				}
			}
		}
	}
	return hasText && hasCommitKey
}

func sensitiveChord(keys []string) bool {
	if len(keys) < 2 {
		return false
	}
	modifier, _, err := input.CompileKeyCombo(keys)
	// Use the same normalized modifier identity as HID compilation. Shift
	// alone is ordinary typing; Control, Alt and Meta require confirmation.
	return err == nil && modifier&0xdd != 0
}

func controlCapabilityTool(capability string) (string, bool) {
	switch capability {
	case "video":
		return "jetkvm_observe", true
	case "input":
		return "jetkvm_run_actions", true
	case "power":
		return "jetkvm_get_power_state", true
	default:
		return "", false
	}
}

func validateAutomationBatch(batch input.Batch) error {
	hasDeviceWrite := false
	for _, action := range batch.Actions {
		switch action.Type {
		case input.ActionScreenshot:
			// The bounded input executor permits screenshots only at the end.
		case input.ActionWait:
		default:
			hasDeviceWrite = true
		}
	}
	if !hasDeviceWrite {
		return fmt.Errorf("%w: batch must contain a device input action", input.ErrInvalidAction)
	}
	return nil
}

func readPowerState(ctx context.Context, session runtimeSession, deviceID domain.DeviceID, observedAt time.Time, out *PowerState) error {
	var extension string
	if err := session.CallRPC(ctx, "getActiveExtension", nil, &extension); err != nil {
		return err
	}
	if extension != "atx-power" {
		return fmt.Errorf("%w: active extension is %q", domain.ErrCapabilityUnavailable, extension)
	}
	var wire struct {
		Power bool `json:"power"`
		HDD   bool `json:"hdd"`
	}
	if err := session.CallRPC(ctx, "getATXState", nil, &wire); err != nil {
		return err
	}
	*out = PowerState{
		DeviceID: deviceID, ActiveExtension: extension,
		PowerLED: wire.Power, HDDLED: wire.HDD, ObservedAt: observedAt,
	}
	return nil
}

func powerWireAction(action PowerAction) (string, bool) {
	switch action {
	case PowerPress:
		return "power-short", true
	case PowerHold:
		return "power-long", true
	case PowerReset:
		return "reset", true
	default:
		return "", false
	}
}

func powerActionRequiresConfirmation(action PowerAction) bool {
	return action == PowerReset || action == PowerHold
}

func canonicalRunActions(request RunActionsRequest) ([]byte, error) {
	return canonicalJSON(struct {
		SchemaVersion int              `json:"schema_version"`
		DeviceID      domain.DeviceID  `json:"device_id"`
		HandleID      control.HandleID `json:"control_handle"`
		Generation    uint64           `json:"generation"`
		Batch         input.Batch      `json:"batch"`
		ObserveAfter  bool             `json:"observe_after,omitzero"`
	}{1, request.DeviceID, request.Ref.ID, request.Ref.ExpectedGeneration, request.Batch, request.ObserveAfter})
}

func canonicalPowerAction(request PowerActionRequest) ([]byte, error) {
	return canonicalJSON(struct {
		SchemaVersion int              `json:"schema_version"`
		DeviceID      domain.DeviceID  `json:"device_id"`
		HandleID      control.HandleID `json:"control_handle"`
		Generation    uint64           `json:"generation"`
		Action        PowerAction      `json:"action"`
	}{1, request.DeviceID, request.Ref.ID, request.Ref.ExpectedGeneration, request.Action})
}

func powerConfirmationBinding(request PowerActionRequest, canonical []byte, policyRevision string) confirmation.Binding {
	return confirmation.Binding{
		DeviceID: request.DeviceID, Generation: request.Ref.ExpectedGeneration,
		Effect: domain.EffectPower, Action: "power." + string(request.Action),
		ArgumentsDigest: confirmation.DigestArguments(canonical), PolicyRevision: policyRevision,
	}
}

func canonicalJSON(value any) ([]byte, error) {
	// Canonical operation digests use exact integer nanoseconds, including wait
	// actions and confirmation lifetimes. JSON v2 has no default duration format.
	encoded, err := json.Marshal(value, json.Deterministic(true), json.WithMarshalers(
		json.MarshalFunc(func(duration time.Duration) ([]byte, error) {
			return json.Marshal(int64(duration))
		}),
	))
	if err != nil {
		return nil, fmt.Errorf("marshal canonical operation arguments: %w", err)
	}
	return encoded, nil
}

func acceptedPatch() operation.Patch {
	return operation.Patch{
		Delivery:      operation.DeliveryTransportAccepted,
		Verification:  operation.Verification{Status: operation.VerificationNotRequested},
		TerminalClaim: operation.TerminalClaimNotProven,
		RetrySafe:     false,
	}
}

func (s *Service) finishActionFailure(ctx context.Context, operationID uuid.UUID, started bool, batch input.BatchReceipt, cause error) (operation.Receipt, error) {
	if !started {
		return s.finishNotSent(ctx, operationID, cause)
	}
	if batch.Status == input.BatchPartial && batch.Neutralized {
		if _, err := s.operations.Transition(ctx, operationID, operation.StageTransportAccepted, acceptedPatch()); err != nil {
			return operation.Receipt{}, err
		}
		return s.operations.Transition(ctx, operationID, operation.StageFailed, operation.Patch{
			Delivery:      operation.DeliveryTransportAccepted,
			Verification:  operation.Verification{Status: operation.VerificationNotRequested},
			TerminalClaim: operation.TerminalClaimNotProven,
			RetrySafe:     false, ErrorKind: errorKind(cause),
		})
	}
	return s.operations.Transition(ctx, operationID, operation.StageAmbiguous, operation.Patch{
		Delivery:      operation.DeliveryPossiblySent,
		Verification:  operation.Verification{Status: operation.VerificationNotRequested},
		TerminalClaim: operation.TerminalClaimNotProven,
		RetrySafe:     false, ErrorKind: errorKind(cause),
	})
}

func (s *Service) finishPowerFailure(ctx context.Context, operationID uuid.UUID, started bool, cause error) (operation.Receipt, error) {
	if !started {
		return s.finishNotSent(ctx, operationID, cause)
	}
	if _, ok := errors.AsType[*jetkvm.RPCError](cause); ok {
		if _, err := s.operations.Transition(ctx, operationID, operation.StageTransportAccepted, acceptedPatch()); err != nil {
			return operation.Receipt{}, err
		}
		return s.operations.Transition(ctx, operationID, operation.StageFailed, operation.Patch{
			Delivery:      operation.DeliveryTransportAccepted,
			Verification:  operation.Verification{Status: operation.VerificationNotRequested},
			TerminalClaim: operation.TerminalClaimNotProven,
			RetrySafe:     false, ErrorKind: errorKind(cause),
		})
	}
	return s.operations.Transition(ctx, operationID, operation.StageAmbiguous, operation.Patch{
		Delivery:      operation.DeliveryPossiblySent,
		Verification:  operation.Verification{Status: operation.VerificationNotRequested},
		TerminalClaim: operation.TerminalClaimNotProven,
		RetrySafe:     false, ErrorKind: errorKind(cause),
	})
}

func (s *Service) finishNotSent(ctx context.Context, operationID uuid.UUID, cause error) (operation.Receipt, error) {
	stage := operation.StageFailed
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		stage = operation.StageCancelled
	}
	return s.operations.Transition(ctx, operationID, stage, operation.Patch{
		Delivery:      operation.DeliveryNotSent,
		Verification:  operation.Verification{Status: operation.VerificationNotRequested},
		TerminalClaim: operation.TerminalClaimNotProven,
		RetrySafe:     true, ErrorKind: errorKind(cause),
	})
}

func errorKind(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, domain.ErrCapabilityUnavailable):
		return "capability_unavailable"
	case errors.Is(err, control.ErrGenerationMismatch), errors.Is(err, input.ErrStaleGeneration):
		return "control_generation_mismatch"
	default:
		return "device_operation_failed"
	}
}
