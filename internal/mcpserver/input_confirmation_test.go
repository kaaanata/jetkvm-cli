package mcpserver

import (
	"testing"
	"time"
	"uuid"

	"github.com/kaaanata/jetkvm-cli/internal/input"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestInputToolsUseDomainConfirmationPlan(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[string]any
	}{
		{toolKeyPress, map[string]any{"key": "ESC"}},
		{toolKeyCombo, map[string]any{"keys": []string{"CMD", "A"}}},
		{toolTypeText, map[string]any{"text": "test"}},
		{toolRunActions, map[string]any{"actions": []map[string]any{{"type": "keypress", "keys": []string{"CTRL", "A"}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issuer, err := NewConfirmationIssuer([]byte("0123456789abcdef0123456789abcdef"), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			adapter, err := New(&fakeDeviceService{}, Options{Automation: &fakeAutomationService{}, ConfirmationIssuer: issuer, PolicyRevision: "policy-test"})
			if err != nil {
				t.Fatal(err)
			}
			session := connectInMemoryWithOptions(t, adapter.newProtocolServer(), &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{}}, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true}})
			args := map[string]any{"device_id": "device-1", "control_handle": "ctl-1", "expected_generation": 7, "operation_id": uuid.NewV7().String()}
			for k, v := range tc.fields {
				args[k] = v
			}
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: tc.name, Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			if !result.NeedsInput() || result.RequestState == "" {
				t.Fatalf("domain confirmation plan ignored: %+v", result)
			}
		})
	}
}

func TestInputBatchRejectsDurationOverflow(t *testing.T) {
	// This positive millisecond value wraps to a short valid duration without
	// checking the multiplication boundary first.
	_, err := inputBatch(runActionsInput{Actions: []actionInput{{Type: input.ActionWait, DurationMS: 18446744073710}}})
	if err == nil {
		t.Fatal("overflowed duration was accepted")
	}
}

func TestOpenControlRejectsDurationOverflowBeforeConfirmation(t *testing.T) {
	for _, milliseconds := range []int64{-1, 18446744073710} {
		server := &Server{}
		if _, _, err := server.openControl(t.Context(), nil, openControlInput{IdleTimeoutMS: milliseconds}); err == nil {
			t.Fatalf("invalid idle timeout %d accepted", milliseconds)
		}
	}
}
