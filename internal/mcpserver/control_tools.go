package mcpserver

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"
	"uuid"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/kaaanata/jetkvm-cli/internal/automation"
	"github.com/kaaanata/jetkvm-cli/internal/confirmation"
	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/input"
	"github.com/kaaanata/jetkvm-cli/internal/jetkvm"
	"github.com/kaaanata/jetkvm-cli/internal/operation"
	"github.com/kaaanata/jetkvm-cli/internal/video"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	toolOpenControl   = "jetkvm_open_control"
	toolGetControl    = "jetkvm_get_control"
	toolCloseControl  = "jetkvm_close_control"
	toolKeyPress      = "jetkvm_key_press"
	toolKeyCombo      = "jetkvm_key_combo"
	toolTypeText      = "jetkvm_type_text"
	toolPointerClick  = "jetkvm_pointer_click"
	toolRunActions    = "jetkvm_run_actions"
	toolGetPowerState = "jetkvm_get_power_state"
	toolPowerAction   = "jetkvm_power_action"

	confirmationInputID = "confirm"
)

type openControlInput struct {
	DeviceID              domain.DeviceID `json:"device_id" jsonschema:"Stable JetKVM hardware identity; aliases are not accepted."`
	RequestedCapabilities []string        `json:"requested_capabilities" jsonschema:"Control capabilities requested for this handle."`
	IdleTimeoutMS         int64           `json:"idle_timeout_ms,omitempty" jsonschema:"Optional idle timeout in milliseconds; zero uses the server default."`
}

type controlRefInput struct {
	DeviceID           domain.DeviceID  `json:"device_id" jsonschema:"Stable JetKVM hardware identity; aliases are not accepted."`
	ControlHandle      control.HandleID `json:"control_handle" jsonschema:"Opaque control handle returned by jetkvm_open_control."`
	ExpectedGeneration uint64           `json:"expected_generation" jsonschema:"Exact control generation used to fence stale calls."`
}

type controlOutput struct {
	ControlHandle     control.HandleID    `json:"control_handle"`
	DeviceID          domain.DeviceID     `json:"device_id"`
	Generation        uint64              `json:"generation"`
	Ownership         control.Ownership   `json:"ownership"`
	Capabilities      []string            `json:"capabilities"`
	State             control.HandleState `json:"state"`
	CreatedAt         time.Time           `json:"created_at"`
	LastUsedAt        time.Time           `json:"last_used_at"`
	IdleExpiresAt     time.Time           `json:"idle_expires_at"`
	AbsoluteExpiresAt time.Time           `json:"absolute_expires_at"`
}

type controlSnapshotOutput struct {
	Transport control.TransportState `json:"transport"`
	Session   control.SessionState   `json:"session"`
	Handle    *controlOutput         `json:"handle,omitempty"`
}

type keyPressInput struct {
	controlRefInput
	OperationID string `json:"operation_id" jsonschema:"Caller supplied UUID used for exactly-once operation lookup."`
	Key         string `json:"key" jsonschema:"One US-layout key name."`
}

type keyComboInput struct {
	controlRefInput
	OperationID string   `json:"operation_id" jsonschema:"Caller supplied UUID used for exactly-once operation lookup."`
	Keys        []string `json:"keys" jsonschema:"Ordered keys in one bounded chord."`
}

type typeTextInput struct {
	controlRefInput
	OperationID string `json:"operation_id" jsonschema:"Caller supplied UUID used for exactly-once operation lookup."`
	Text        string `json:"text" jsonschema:"Printable US-layout text. Control characters are rejected."`
}

type pointerClickInput struct {
	controlRefInput
	OperationID     string       `json:"operation_id" jsonschema:"Caller supplied UUID used for exactly-once operation lookup."`
	ObservationID   string       `json:"observation_id" jsonschema:"Observation identity that the coordinates were derived from."`
	X               int          `json:"x"`
	Y               int          `json:"y"`
	FrameWidth      int          `json:"frame_width,omitempty"`
	FrameHeight     int          `json:"frame_height,omitempty"`
	ObservationTime time.Time    `json:"observation_captured_at,omitzero"`
	Button          input.Button `json:"button,omitempty"`
	ObserveAfter    bool         `json:"observe_after,omitzero"`
}

type actionInput struct {
	Type       input.ActionType `json:"type"`
	X          int              `json:"x,omitempty"`
	Y          int              `json:"y,omitempty"`
	Button     input.Button     `json:"button,omitempty"`
	Path       []input.Point    `json:"path,omitempty"`
	DeltaX     int              `json:"delta_x,omitempty"`
	DeltaY     int              `json:"delta_y,omitempty"`
	Keys       []string         `json:"keys,omitempty"`
	Text       string           `json:"text,omitempty"`
	DurationMS int64            `json:"duration_ms,omitempty"`
}

type runActionsInput struct {
	ObserveAfter bool `json:"observe_after,omitzero"`
	controlRefInput
	OperationID     string        `json:"operation_id" jsonschema:"Caller supplied UUID used for exactly-once operation lookup."`
	ObservationID   string        `json:"observation_id,omitempty"`
	FrameWidth      int           `json:"frame_width,omitempty"`
	FrameHeight     int           `json:"frame_height,omitempty"`
	ObservationTime time.Time     `json:"observation_captured_at,omitempty"`
	Actions         []actionInput `json:"actions" jsonschema:"One to sixteen deterministic input actions."`
}

type powerActionInput struct {
	controlRefInput
	ExpectedDeviceID domain.DeviceID        `json:"expected_device_id" jsonschema:"Must exactly repeat device_id for destructive target confirmation."`
	OperationID      string                 `json:"operation_id" jsonschema:"Caller supplied UUID used for exactly-once operation lookup."`
	Action           automation.PowerAction `json:"action"`
	Reason           string                 `json:"reason,omitempty"`
}

type operationReceiptOutput struct {
	OperationID    string                 `json:"operation_id"`
	DeviceID       domain.DeviceID        `json:"device_id"`
	Generation     uint64                 `json:"generation"`
	Effect         domain.EffectClass     `json:"effect"`
	Action         string                 `json:"action"`
	PolicyRevision string                 `json:"policy_revision"`
	Stage          operation.Stage        `json:"stage"`
	Delivery       operation.Delivery     `json:"delivery"`
	Verification   operation.Verification `json:"verification"`
	TerminalClaim  string                 `json:"terminal_claim"`
	RetrySafe      bool                   `json:"retry_safe"`
	ErrorKind      string                 `json:"error_kind,omitempty"`
	Warnings       []string               `json:"warnings,omitempty"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
	SendStartedAt  time.Time              `json:"send_started_at,omitempty"`
	TerminalAt     time.Time              `json:"terminal_at,omitempty"`
}

type operationOutput struct {
	Operation operationReceiptOutput `json:"operation"`
}

type runActionsOutput struct {
	Observation *automation.ScreenObservation `json:"observation,omitempty"`
	Operation   operationReceiptOutput        `json:"operation"`
	Batch       input.BatchReceipt            `json:"batch,omitzero"`
	Existing    bool                          `json:"existing"`
}

type powerStateOutput struct {
	Power automation.PowerState `json:"power"`
}

func (s *Server) registerControlTools(server *mcp.Server) {
	if !s.controlToolsReady() {
		return
	}
	s.registerPointerTools(server)
	register := func(name string, tool *mcp.Tool, add func()) {
		if s.toolAllowed(name) {
			add()
		}
	}

	register(toolOpenControl, stateChangingTool(toolOpenControl, "Open JetKVM control", "Opens an explicitly confirmed, fenced WebRTC control handle.", true), func() {
		mcp.AddTool(server, stateChangingTool(toolOpenControl, "Open JetKVM control", "Opens an explicitly confirmed, fenced WebRTC control handle.", true), s.openControl)
	})
	register(toolGetControl, readOnlyTool(toolGetControl, "Get JetKVM control", "Reads one fenced control handle without creating a session."), func() {
		mcp.AddTool(server, readOnlyTool(toolGetControl, "Get JetKVM control", "Reads one fenced control handle without creating a session."), s.getControl)
	})
	register(toolCloseControl, stateChangingTool(toolCloseControl, "Close JetKVM control", "Closes an owned control handle after input neutralization.", true), func() {
		mcp.AddTool(server, stateChangingTool(toolCloseControl, "Close JetKVM control", "Closes an owned control handle after input neutralization.", true), s.closeControl)
	})
	register(toolKeyPress, stateChangingTool(toolKeyPress, "Press one key", "Presses and releases one validated US-layout key.", false), func() {
		mcp.AddTool(server, stateChangingTool(toolKeyPress, "Press one key", "Presses and releases one validated US-layout key.", false), s.keyPress)
	})
	register(toolKeyCombo, stateChangingTool(toolKeyCombo, "Press a key chord", "Presses and releases one bounded key chord.", false), func() {
		mcp.AddTool(server, stateChangingTool(toolKeyCombo, "Press a key chord", "Presses and releases one bounded key chord.", false), s.keyCombo)
	})
	register(toolTypeText, stateChangingTool(toolTypeText, "Type text", "Types fully prevalidated printable US-layout text.", false), func() {
		mcp.AddTool(server, stateChangingTool(toolTypeText, "Type text", "Types fully prevalidated printable US-layout text.", false), s.typeText)
	})
	register(toolPointerClick, stateChangingTool(toolPointerClick, "Click an observed point", "Clicks coordinates bound to one fresh observation and generation.", false), func() {
		mcp.AddTool(server, stateChangingTool(toolPointerClick, "Click an observed point", "Clicks coordinates bound to one fresh observation and generation.", false), s.pointerClick)
	})
	register(toolRunActions, stateChangingTool(toolRunActions, "Run input actions", "Runs a bounded deterministic computer-use action batch.", false), func() {
		mcp.AddTool(server, stateChangingTool(toolRunActions, "Run input actions", "Runs a bounded deterministic computer-use action batch.", false), s.runActions)
	})
	register(toolGetPowerState, readOnlyTool(toolGetPowerState, "Get ATX power state", "Reads current ATX extension LEDs through the fenced control handle."), func() {
		mcp.AddTool(server, readOnlyTool(toolGetPowerState, "Get ATX power state", "Reads current ATX extension LEDs through the fenced control handle."), s.getPowerState)
	})
	register(toolPowerAction, stateChangingTool(toolPowerAction, "Perform ATX power action", "Performs one non-retryable ATX press, reset, or hold action.", true), func() {
		mcp.AddTool(server, stateChangingTool(toolPowerAction, "Perform ATX power action", "Performs one non-retryable ATX press, reset, or hold action.", true), s.powerAction)
	})
}

func stateChangingTool(name, title, description string, destructive bool) *mcp.Tool {
	return &mcp.Tool{
		Name: name, Title: title, Description: description,
		Annotations: &mcp.ToolAnnotations{
			Title: title, ReadOnlyHint: false, DestructiveHint: new(destructive),
			IdempotentHint: false, OpenWorldHint: new(false),
		},
	}
}

func (s *Server) openControl(ctx context.Context, req *mcp.CallToolRequest, in openControlInput) (*mcp.CallToolResult, controlOutput, error) {
	if in.IdleTimeoutMS < 0 || in.IdleTimeoutMS > int64((1<<63-1)/time.Millisecond) {
		return nil, controlOutput{}, errors.New("idle_timeout_ms is out of range")
	}
	request := automation.OpenControlRequest{
		DeviceID: in.DeviceID, Capabilities: in.RequestedCapabilities, Scope: s.scope,
		Ownership: control.OwnershipOwned, IdleTimeout: time.Duration(in.IdleTimeoutMS) * time.Millisecond,
	}
	plan, err := s.automation.PrepareOpenControl(request)
	if err != nil {
		return nil, controlOutput{}, err
	}
	executionContext := ctx
	if plan.Required {
		claims, err := s.confirmationClaims(req, plan.Binding, "", "")
		if err != nil {
			return nil, controlOutput{}, err
		}
		confirmedContext, pending, err := s.confirmCall(ctx, req, claims, "Confirm opening JetKVM control for device "+string(in.DeviceID)+". This may disconnect an existing browser session.")
		if pending != nil || err != nil {
			return pending, controlOutput{}, err
		}
		executionContext = confirmedContext
	}
	handle, err := s.automation.OpenControl(executionContext, request)
	if err != nil {
		return nil, controlOutput{}, publicAutomationError(err)
	}
	return nil, controlView(handle), nil
}

func (s *Server) getControl(ctx context.Context, _ *mcp.CallToolRequest, in controlRefInput) (*mcp.CallToolResult, controlSnapshotOutput, error) {
	snapshot, err := s.automation.GetControl(ctx, automation.ControlRequest{DeviceID: in.DeviceID, Ref: controlRef(in), Scope: s.scope})
	if err != nil {
		return nil, controlSnapshotOutput{}, publicAutomationError(err)
	}
	return nil, controlSnapshotView(snapshot), nil
}

func (s *Server) closeControl(ctx context.Context, _ *mcp.CallToolRequest, in controlRefInput) (*mcp.CallToolResult, controlOutput, error) {
	handle, err := s.automation.CloseControl(ctx, automation.ControlRequest{DeviceID: in.DeviceID, Ref: controlRef(in), Scope: s.scope})
	if err != nil {
		return nil, controlOutput{}, publicAutomationError(err)
	}
	return nil, controlView(handle), nil
}

func (s *Server) keyPress(ctx context.Context, req *mcp.CallToolRequest, in keyPressInput) (*mcp.CallToolResult, runActionsOutput, error) {
	return s.executePlannedActions(ctx, req, in.controlRefInput, in.OperationID, input.Batch{Actions: []input.Action{{Type: input.ActionKeypress, Keys: []string{in.Key}}}})
}

func (s *Server) keyCombo(ctx context.Context, req *mcp.CallToolRequest, in keyComboInput) (*mcp.CallToolResult, runActionsOutput, error) {
	batch := input.Batch{Actions: []input.Action{{Type: input.ActionKeypress, Keys: in.Keys}}}
	return s.executePlannedActions(ctx, req, in.controlRefInput, in.OperationID, batch)
}

func (s *Server) typeText(ctx context.Context, req *mcp.CallToolRequest, in typeTextInput) (*mcp.CallToolResult, runActionsOutput, error) {
	batch := input.Batch{Actions: []input.Action{{Type: input.ActionTypeText, Text: in.Text}}}
	return s.executePlannedActions(ctx, req, in.controlRefInput, in.OperationID, batch)
}

func (s *Server) pointerClick(ctx context.Context, _ *mcp.CallToolRequest, in pointerClickInput) (*mcp.CallToolResult, runActionsOutput, error) {
	button := in.Button
	if button == "" {
		button = input.ButtonLeft
	}
	batch := input.Batch{
		Observation: &input.ObservationBinding{ID: in.ObservationID, Generation: in.ExpectedGeneration},
		Actions:     []input.Action{{Type: input.ActionClick, X: in.X, Y: in.Y, Button: button}},
	}
	return s.executeActions(ctx, in.controlRefInput, in.OperationID, batch, in.ObserveAfter)
}

func (s *Server) runActions(ctx context.Context, req *mcp.CallToolRequest, in runActionsInput) (*mcp.CallToolResult, runActionsOutput, error) {
	batch, err := inputBatch(in)
	if err != nil {
		return nil, runActionsOutput{}, err
	}
	return s.executePlannedActions(ctx, req, in.controlRefInput, in.OperationID, batch, in.ObserveAfter)
}

func (s *Server) executePlannedActions(ctx context.Context, req *mcp.CallToolRequest, ref controlRefInput, operationID string, batch input.Batch, observeAfter ...bool) (*mcp.CallToolResult, runActionsOutput, error) {
	id, err := parseOperationID(operationID)
	if err != nil {
		return nil, runActionsOutput{}, err
	}
	request := automation.RunActionsRequest{DeviceID: ref.DeviceID, Ref: controlRef(ref), Scope: s.scope, OperationID: id, Batch: batch}
	request.ObserveAfter = len(observeAfter) > 0 && observeAfter[0]
	plan, err := s.automation.PrepareRunActions(request)
	if err != nil {
		return nil, runActionsOutput{}, publicAutomationError(err)
	}
	if !plan.Required {
		return s.executeActions(ctx, ref, operationID, batch, observeAfter...)
	}
	observationID := ""
	if batch.Observation != nil {
		observationID = batch.Observation.ID
	}
	claims, err := s.confirmationClaims(req, plan.Binding, ref.ControlHandle, observationID)
	if err != nil {
		return nil, runActionsOutput{}, err
	}
	confirmedCtx, pending, err := s.confirmCall(ctx, req, claims, "Confirm input commit on device "+string(ref.DeviceID)+". The complete action batch is bound to this one-time confirmation.")
	if pending != nil || err != nil {
		return pending, runActionsOutput{}, err
	}
	return s.executeActions(confirmedCtx, ref, operationID, batch, observeAfter...)
}

func (s *Server) executeActions(ctx context.Context, ref controlRefInput, operationID string, batch input.Batch, observeAfter ...bool) (*mcp.CallToolResult, runActionsOutput, error) {
	id, err := parseOperationID(operationID)
	if err != nil {
		return nil, runActionsOutput{}, err
	}
	result, err := s.automation.RunActions(ctx, automation.RunActionsRequest{
		DeviceID: ref.DeviceID, Ref: controlRef(ref), Scope: s.scope, OperationID: id, Batch: batch,
		ObserveAfter: len(observeAfter) > 0 && observeAfter[0],
	})
	output := runActionsOutput{Operation: receiptView(result.Operation), Batch: result.Batch, Existing: result.Existing, Observation: result.Observation}
	response := &mcp.CallToolResult{StructuredContent: output}
	if result.Observation != nil {
		response.Content = append(response.Content, screenImage(*result.Observation))
	}
	if err != nil {
		response.IsError = true
		response.Content = append(response.Content, &mcp.TextContent{Text: publicAutomationError(err).Error()})
	}
	return response, output, nil
}

func (s *Server) getPowerState(ctx context.Context, _ *mcp.CallToolRequest, in controlRefInput) (*mcp.CallToolResult, powerStateOutput, error) {
	state, err := s.automation.GetPowerState(ctx, automation.ControlRequest{DeviceID: in.DeviceID, Ref: controlRef(in), Scope: s.scope})
	if err != nil {
		return nil, powerStateOutput{}, publicAutomationError(err)
	}
	return nil, powerStateOutput{Power: state}, nil
}

func (s *Server) powerAction(ctx context.Context, req *mcp.CallToolRequest, in powerActionInput) (*mcp.CallToolResult, operationOutput, error) {
	if in.ExpectedDeviceID != in.DeviceID {
		return nil, operationOutput{}, errors.New("expected_device_id must exactly match device_id")
	}
	id, err := parseOperationID(in.OperationID)
	if err != nil {
		return nil, operationOutput{}, err
	}
	request := automation.PowerActionRequest{
		DeviceID: in.DeviceID, Ref: controlRef(in.controlRefInput), Scope: s.scope, OperationID: id, Action: in.Action,
	}
	plan, err := s.automation.PreparePowerAction(request)
	if err != nil {
		return nil, operationOutput{}, publicAutomationError(err)
	}
	if plan.Required {
		claims, err := s.confirmationClaims(req, plan.Binding, in.ControlHandle, "")
		if err != nil {
			return nil, operationOutput{}, err
		}
		confirmedCtx, pending, err := s.confirmCall(ctx, req, claims, "Confirm "+string(in.Action)+" for JetKVM device "+string(in.DeviceID)+". This physical action is non-retryable.")
		if pending != nil || err != nil {
			return pending, operationOutput{}, err
		}
		ctx = confirmedCtx
	}
	receipt, err := s.automation.PowerAction(ctx, request)
	output := operationOutput{Operation: receiptView(receipt)}
	if err != nil {
		return nil, output, publicAutomationError(err)
	}
	return nil, output, nil
}

func (s *Server) confirmationClaims(req *mcp.CallToolRequest, binding confirmation.Binding, handle control.HandleID, observationID string) (ConfirmationRequest, error) {
	if !s.confirmationReady() {
		return ConfirmationRequest{}, errors.New("confirmation authority is unavailable")
	}
	principal := ""
	if info := req.ClientInfo(); info != nil {
		principal = info.Name + "/" + info.Version
	}
	return ConfirmationRequest{
		Principal: principal, DeviceID: binding.DeviceID, ControlHandle: handle, Generation: binding.Generation,
		OperationKind: binding.Action, ArgumentsDigest: hex.EncodeToString(binding.ArgumentsDigest[:]), ObservationID: observationID,
		PolicyRevision: binding.PolicyRevision, Binding: binding,
	}, nil
}

func (s *Server) confirmCall(ctx context.Context, req *mcp.CallToolRequest, claims ConfirmationRequest, message string) (context.Context, *mcp.CallToolResult, error) {
	response, responded := req.Params.InputResponses[confirmationInputID]
	if !responded {
		capabilities := req.ClientCapabilities()
		if capabilities == nil || capabilities.Elicitation == nil {
			return ctx, nil, errors.New("client does not support elicitation required for this operation")
		}
		state, err := s.confirm.Issue(ctx, claims)
		if err != nil {
			return ctx, nil, errors.New("failed to issue confirmation")
		}
		return ctx, &mcp.CallToolResult{
			InputRequests: mcp.InputRequestMap{
				confirmationInputID: &mcp.ElicitParams{
					Mode: "form", Message: message,
					RequestedSchema: confirmationSchema(claims.DeviceID),
				},
			},
			RequestState: state,
		}, nil
	}
	elicitation, ok := response.(*mcp.ElicitResult)
	if !ok || elicitation == nil || elicitation.Action != "accept" {
		return ctx, nil, errors.New("operation confirmation was declined or invalid")
	}
	confirmed, _ := elicitation.Content["confirmed"].(bool)
	confirmedDevice, _ := elicitation.Content["device_id"].(string)
	if !confirmed || confirmedDevice != string(claims.DeviceID) {
		return ctx, nil, errors.New("operation confirmation did not match the target device")
	}
	confirmedCtx, err := s.confirm.Confirm(ctx, req.Params.RequestState, claims)
	if err != nil {
		return ctx, nil, errors.New("operation confirmation is invalid, expired, or already used")
	}
	return confirmedCtx, nil, nil
}

func confirmationSchema(deviceID domain.DeviceID) *jsonschema.Schema {
	confirmed := any(true)
	device := any(string(deviceID))
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"confirmed": {Type: "boolean", Const: &confirmed},
			"device_id": {Type: "string", Const: &device},
		},
		Required:             []string{"confirmed", "device_id"},
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
	}
}

func inputBatch(in runActionsInput) (input.Batch, error) {
	actions := make([]input.Action, len(in.Actions))
	for index, action := range in.Actions {
		if action.DurationMS < 0 || action.DurationMS > int64((1<<63-1)/time.Millisecond) {
			return input.Batch{}, fmt.Errorf("action %d duration_ms is out of range", index)
		}
		actions[index] = input.Action{
			Type: action.Type, X: action.X, Y: action.Y, Button: action.Button,
			Path: slices.Clone(action.Path), DeltaX: action.DeltaX, DeltaY: action.DeltaY,
			Keys: slices.Clone(action.Keys), Text: action.Text, Duration: time.Duration(action.DurationMS) * time.Millisecond,
		}
	}
	batch := input.Batch{Actions: actions}
	if in.ObservationID != "" {
		batch.Observation = &input.ObservationBinding{
			ID: in.ObservationID, Generation: in.ExpectedGeneration,
		}
	}
	return batch, nil
}

func parseOperationID(value string) (uuid.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil() {
		return uuid.Nil(), errors.New("operation_id must be a non-nil UUID")
	}
	return id, nil
}

func controlRef(in controlRefInput) control.Ref {
	return control.Ref{ID: in.ControlHandle, ExpectedGeneration: in.ExpectedGeneration}
}

func controlView(handle control.Handle) controlOutput {
	return controlOutput{
		ControlHandle: handle.ID, DeviceID: handle.DeviceID, Generation: handle.Generation,
		Ownership: handle.Ownership, Capabilities: slices.Clone(handle.Capabilities), State: handle.State,
		CreatedAt: handle.CreatedAt, LastUsedAt: handle.LastUsedAt, IdleExpiresAt: handle.IdleExpiresAt,
		AbsoluteExpiresAt: handle.AbsoluteExpiresAt,
	}
}

func controlSnapshotView(snapshot control.Snapshot) controlSnapshotOutput {
	output := controlSnapshotOutput{Transport: snapshot.Transport, Session: snapshot.Session}
	if snapshot.Handle != nil {
		handle := controlView(*snapshot.Handle)
		output.Handle = &handle
	}
	return output
}

func receiptView(receipt operation.Receipt) operationReceiptOutput {
	return operationReceiptOutput{
		OperationID: receipt.ID.String(), DeviceID: receipt.DeviceID, Generation: receipt.ControlGeneration,
		Effect: receipt.Effect, Action: receipt.Action, PolicyRevision: receipt.PolicyRevision,
		Stage: receipt.Stage, Delivery: receipt.Delivery, Verification: receipt.Verification,
		TerminalClaim: receipt.TerminalClaim, RetrySafe: receipt.RetrySafe, ErrorKind: receipt.ErrorKind,
		Warnings: slices.Clone(receipt.Warnings), CreatedAt: receipt.CreatedAt, UpdatedAt: receipt.UpdatedAt,
		SendStartedAt: receipt.SendStartedAt, TerminalAt: receipt.TerminalAt,
	}
}

func publicAutomationError(err error) error {
	switch {
	case errors.Is(err, input.ErrUnknownKey):
		return fmt.Errorf("invalid_argument: %w", input.ErrUnknownKey)
	case errors.Is(err, input.ErrUnsupportedText):
		return fmt.Errorf("invalid_argument: %w", input.ErrUnsupportedText)
	case errors.Is(err, input.ErrNeutralization):
		return fmt.Errorf("unavailable: %w", input.ErrNeutralization)
	case errors.Is(err, jetkvm.ErrSessionClosed), errors.Is(err, jetkvm.ErrSessionReplaced):
		return fmt.Errorf("unavailable: %w", jetkvm.ErrSessionClosed)
	case errors.Is(err, video.ErrDecoderUnavailable), errors.Is(err, domain.ErrCapabilityUnavailable):
		return fmt.Errorf("capability_unavailable: %w", domain.ErrCapabilityUnavailable)
	case errors.Is(err, video.ErrFrameStale), errors.Is(err, input.ErrObservationStale):
		return fmt.Errorf("observation_stale: %w", input.ErrObservationStale)
	case errors.Is(err, video.ErrGenerationMismatch):
		return fmt.Errorf("control_generation_mismatch: %w", control.ErrGenerationMismatch)
	case errors.Is(err, video.ErrDecodeFailed):
		return fmt.Errorf("unavailable: %w", video.ErrDecodeFailed)
	case errors.Is(err, video.ErrDimensionsExceeded):
		return fmt.Errorf("unavailable: %w", video.ErrDimensionsExceeded)
	case errors.Is(err, video.ErrVideoUnavailable), errors.Is(err, video.ErrPipelineClosed):
		return fmt.Errorf("unavailable: %w", video.ErrVideoUnavailable)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("unavailable: %w", context.DeadlineExceeded)
	case errors.Is(err, domain.ErrFirmwareUnsupported),
		errors.Is(err, domain.ErrTakeoverDisabled),
		errors.Is(err, control.ErrControlNotFound),
		errors.Is(err, control.ErrControlExpired),
		errors.Is(err, control.ErrGenerationMismatch),
		errors.Is(err, operation.ErrConflict),
		errors.Is(err, input.ErrInvalidAction):
		return err
	default:
		return errors.New("automation request failed")
	}
}
