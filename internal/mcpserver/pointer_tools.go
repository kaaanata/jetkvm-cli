package mcpserver

import (
	"context"

	"github.com/kaaanata/jetkvm-cli/internal/input"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type pointerDragInput struct {
	controlRefInput
	OperationID   string        `json:"operation_id"`
	ObservationID string        `json:"observation_id"`
	Path          []input.Point `json:"path"`
	Button        input.Button  `json:"button,omitempty"`
	ObserveAfter  bool          `json:"observe_after,omitzero"`
}

type pointerScrollInput struct {
	controlRefInput
	OperationID  string `json:"operation_id"`
	DeltaX       int    `json:"delta_x,omitzero"`
	DeltaY       int    `json:"delta_y,omitzero"`
	ObserveAfter bool   `json:"observe_after,omitzero"`
}

func (s *Server) registerPointerTools(server *mcp.Server) {
	for _, definition := range []struct {
		name   string
		action input.ActionType
	}{
		{"jetkvm_pointer_move", input.ActionMove}, {"jetkvm_pointer_double_click", input.ActionDoubleClick},
	} {
		if !s.toolAllowed(definition.name) {
			continue
		}
		mcp.AddTool(server, stateChangingTool(definition.name, "Move or double-click an observed point", "Uses only the server-owned observation_id to bind coordinates.", false), func(ctx context.Context, _ *mcp.CallToolRequest, in pointerClickInput) (*mcp.CallToolResult, runActionsOutput, error) {
			action := input.Action{Type: definition.action, X: in.X, Y: in.Y}
			if definition.action != input.ActionMove {
				action.Button = in.Button
				if action.Button == "" {
					action.Button = input.ButtonLeft
				}
			}
			return s.executeActions(ctx, in.controlRefInput, in.OperationID, input.Batch{Observation: &input.ObservationBinding{ID: in.ObservationID, Generation: in.ExpectedGeneration}, Actions: []input.Action{action}}, in.ObserveAfter)
		})
	}
	if s.toolAllowed("jetkvm_pointer_drag") {
		mcp.AddTool(server, stateChangingTool("jetkvm_pointer_drag", "Drag along an observed path", "Runs one bounded drag bound to a server-owned observation_id.", false), func(ctx context.Context, _ *mcp.CallToolRequest, in pointerDragInput) (*mcp.CallToolResult, runActionsOutput, error) {
			button := in.Button
			if button == "" {
				button = input.ButtonLeft
			}
			return s.executeActions(ctx, in.controlRefInput, in.OperationID, input.Batch{Observation: &input.ObservationBinding{ID: in.ObservationID, Generation: in.ExpectedGeneration}, Actions: []input.Action{{Type: input.ActionDrag, Path: in.Path, Button: button}}}, in.ObserveAfter)
		})
	}
	if s.toolAllowed("jetkvm_pointer_scroll") {
		mcp.AddTool(server, stateChangingTool("jetkvm_pointer_scroll", "Scroll", "Runs one bounded scroll action.", false), func(ctx context.Context, _ *mcp.CallToolRequest, in pointerScrollInput) (*mcp.CallToolResult, runActionsOutput, error) {
			return s.executeActions(ctx, in.controlRefInput, in.OperationID, input.Batch{Actions: []input.Action{{Type: input.ActionScroll, DeltaX: in.DeltaX, DeltaY: in.DeltaY}}}, in.ObserveAfter)
		})
	}
}
