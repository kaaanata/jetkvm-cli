package mcpserver

import (
	"context"
	"errors"
	"time"

	"github.com/kaaanata/jetkvm-cli/internal/automation"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const toolObserve = "jetkvm_observe"
const toolCaptureScreen = "jetkvm_capture_screen"

// Observer is separate from the control-only automation dependency.
type Observer interface {
	Observe(context.Context, automation.ObserveRequest) (automation.ScreenObservation, error)
}

type observeInput struct {
	controlRefInput
	DisableWake     bool   `json:"disable_wake,omitzero" jsonschema:"Disable automatic one-shot waking. By default, a video+input handle may wake sleeping/no-signal video once under existing input policy."`
	WakeOperationID string `json:"wake_operation_id,omitempty" jsonschema:"UUID for deduplication of the optional wake operation."`
	FreshnessMS     int64  `json:"freshness_ms,omitzero"`
}

func (s *Server) registerObservationTools(server *mcp.Server) {
	if _, ok := s.automation.(Observer); !ok || !s.decoder {
		return
	}
	for _, name := range []string{toolObserve, toolCaptureScreen} {
		if s.toolAllowed(name) {
			mcp.AddTool(server, stateChangingTool(name, "Observe JetKVM screen", "Captures a PNG from an existing video handle. Automatically attempts one Shift wake for sleeping/no-signal video when the handle and policy permit input; disable_wake opts out.", false), s.observe)
		}
	}
}

func screenImage(screen automation.ScreenObservation) *mcp.ImageContent {
	return &mcp.ImageContent{Data: screen.Data, MIMEType: screen.MIMEType}
}

func (s *Server) observe(ctx context.Context, _ *mcp.CallToolRequest, in observeInput) (*mcp.CallToolResult, automation.ScreenObservation, error) {
	if in.DeviceID == "" || in.ControlHandle == "" || in.ExpectedGeneration == 0 {
		return nil, automation.ScreenObservation{}, errors.New("device_id, control_handle and non-zero expected_generation are required")
	}
	if in.FreshnessMS < 0 || in.FreshnessMS > int64((1<<63-1)/time.Millisecond) {
		return nil, automation.ScreenObservation{}, errors.New("freshness_ms is out of range")
	}
	observer, ok := s.automation.(Observer)
	if !ok || !s.decoder {
		return nil, automation.ScreenObservation{}, domain.ErrCapabilityUnavailable
	}
	request := automation.ObserveRequest{
		ControlRequest: automation.ControlRequest{DeviceID: in.DeviceID, Ref: controlRef(in.controlRefInput), Scope: s.scope},
		Freshness:      time.Duration(in.FreshnessMS) * time.Millisecond,
		DisableWake:    in.DisableWake,
	}
	if in.WakeOperationID != "" {
		id, err := parseOperationID(in.WakeOperationID)
		if err != nil {
			return nil, automation.ScreenObservation{}, err
		}
		request.WakeOperationID = id
	}
	screen, err := observer.Observe(ctx, request)
	if err != nil {
		if screen.Wake != nil {
			return &mcp.CallToolResult{IsError: true, StructuredContent: screen, Content: []mcp.Content{&mcp.TextContent{Text: publicAutomationError(err).Error()}}}, screen, nil
		}
		return nil, automation.ScreenObservation{}, publicAutomationError(err)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{screenImage(screen)}, StructuredContent: screen}, screen, nil
}
