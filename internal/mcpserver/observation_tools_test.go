package mcpserver

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"
	"uuid"

	"github.com/kaaanata/jetkvm-cli/internal/automation"
	"github.com/kaaanata/jetkvm-cli/internal/input"
	"github.com/kaaanata/jetkvm-cli/internal/operation"
	"github.com/kaaanata/jetkvm-cli/internal/video"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type observingService struct {
	fakeAutomationService
	screen   automation.ScreenObservation
	observed automation.ObserveRequest
	actions  automation.RunActionsRequest
	runErr   error
}

func (f *observingService) Observe(_ context.Context, request automation.ObserveRequest) (automation.ScreenObservation, error) {
	f.observed = request
	return f.screen, nil
}

func (f *observingService) RunActions(_ context.Context, request automation.RunActionsRequest) (automation.RunActionsResult, error) {
	f.actions = request
	return automation.RunActionsResult{Operation: operation.Receipt{Request: operation.Request{ID: request.OperationID, DeviceID: request.DeviceID, ControlGeneration: request.Ref.ExpectedGeneration}}, Observation: &f.screen}, f.runErr
}

func TestObservationAndPartialActionImagesSurviveSDK(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	service := &observingService{screen: automation.ScreenObservation{MIMEType: "image/png", Data: data.Bytes(), Observation: video.Observation{ID: "obs-server", DeviceID: "device-1", Frame: video.FrameMetadata{Generation: 7, Width: 2, Height: 2}}}}
	service.screen.Observation.Image = image.NewRGBA(image.Rect(0, 0, 2, 2))
	adapter, err := New(&fakeDeviceService{}, Options{Automation: service, DecoderAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	session := connectInMemory(t, adapter.newProtocolServer())
	for _, name := range []string{toolObserve, toolCaptureScreen, toolRunActions, toolPointerClick, "jetkvm_pointer_move", "jetkvm_pointer_double_click", "jetkvm_pointer_drag", "jetkvm_pointer_scroll"} {
		t.Run(name, func(t *testing.T) {
			args := map[string]any{"device_id": "device-1", "control_handle": "ctl_test", "expected_generation": 7}
			isObserve := name == toolObserve || name == toolCaptureScreen
			id := uuid.NewV7().String()
			if !isObserve {
				args["operation_id"], args["observe_after"] = id, true
				service.runErr = errors.New("partial execution")
				switch name {
				case toolRunActions:
					args["actions"] = []map[string]any{{"type": "screenshot"}}
				case "jetkvm_pointer_drag":
					args["observation_id"] = "obs-server"
					args["path"] = []map[string]int{{"x": 0, "y": 0}, {"x": 1, "y": 1}}
				case "jetkvm_pointer_scroll":
					args["delta_y"] = 1
				default:
					args["observation_id"], args["x"], args["y"] = "obs-server", 1, 1
				}
			}
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			if result.IsError != !isObserve {
				t.Fatalf("unexpected error state: %+v", result)
			}
			found := false
			for _, content := range result.Content {
				if image, ok := content.(*mcp.ImageContent); ok {
					found = true
					if image.MIMEType != "image/png" || !bytes.Equal(image.Data, data.Bytes()) {
						t.Fatal("image payload changed")
					}
					if _, err := png.Decode(bytes.NewReader(image.Data)); err != nil {
						t.Fatal(err)
					}
				}
			}
			if !found {
				t.Fatalf("no ImageContent: %+v", result)
			}
			metadata, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(metadata), "obs-server") || strings.Contains(string(metadata), `"data"`) || strings.Contains(string(metadata), `"Image"`) || strings.Contains(string(metadata), `"image"`) {
				t.Fatalf("metadata=%s", metadata)
			}
			if !isObserve && (!service.actions.ObserveAfter || !strings.Contains(string(metadata), id)) {
				t.Fatalf("partial receipt lost: %s", metadata)
			}
		})
	}
	if service.observed.Ref.ID != "ctl_test" || service.observed.Ref.ExpectedGeneration != 7 {
		t.Fatal("observation fence lost")
	}
}

func TestPointerBindingUsesOnlyObservationID(t *testing.T) {
	batch, err := inputBatch(runActionsInput{controlRefInput: controlRefInput{ExpectedGeneration: 7}, ObservationID: "obs-owned", FrameWidth: 999, FrameHeight: 999, Actions: []actionInput{{Type: input.ActionMove}}})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Observation.ID != "obs-owned" || batch.Observation.Width != 0 || batch.Observation.Height != 0 || !batch.Observation.CapturedAt.IsZero() {
		t.Fatalf("caller metadata forwarded: %+v", batch.Observation)
	}
}
