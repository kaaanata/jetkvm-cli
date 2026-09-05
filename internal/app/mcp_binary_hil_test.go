package app

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"uuid"

	"github.com/kaaanata/jetkvm-cli/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestHILMCPBinary exercises the installed/candidate executable and its real
// confirmation authority. Explicit HIL opt-in authorizes the single configured
// target; no persistent policy is weakened and no power action is performed.
func TestHILMCPBinary(t *testing.T) {
	binary, path, output := os.Getenv("JETKVM_HIL_BINARY"), os.Getenv("JETKVM_HIL_CONFIG"), os.Getenv("JETKVM_HIL_SCREEN")
	if os.Getenv("JETKVM_HIL_MCP") != "1" || binary == "" || path == "" || output == "" {
		t.Skip("binary MCP HIL not requested")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("HIL binary must be absolute")
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Devices) != 1 {
		t.Fatal("HIL requires one explicit device")
	}
	var deviceID string
	for _, d := range cfg.Devices {
		deviceID = d.DeviceID
		if !d.Takeover.RequireConfirmation {
			t.Fatal("MCP HIL requires takeover confirmation")
		}
	}
	dir := t.TempDir()
	cfg.State.Path = filepath.Join(dir, "state.db")
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	confirmations := 0
	client := mcp.NewClient(&mcp.Implementation{Name: "jetkvm-binary-hil", Version: "1"}, &mcp.ClientOptions{
		ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			// Accept only the exact target bound by the server's takeover schema.
			var schema struct {
				Properties map[string]struct {
					Const any `json:"const"`
				} `json:"properties"`
			}
			raw, err := json.Marshal(req.Params.RequestedSchema)
			if err != nil {
				return nil, err
			}
			if err := json.Unmarshal(raw, &schema); err != nil {
				return nil, err
			}
			if req.Params.Mode != "form" || schema.Properties["device_id"].Const != deviceID || req.Params.Message != "Confirm opening JetKVM control for device "+deviceID+". This may disconnect an existing browser session." {
				t.Error("unexpected confirmation target")
				return &mcp.ElicitResult{Action: "decline"}, nil
			}
			confirmations++
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirmed": true, "device_id": deviceID}}, nil
		},
	})
	command := exec.CommandContext(ctx, binary, "--config", configPath, "mcp", "serve", "--transport=stdio")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Errorf("MCP shutdown: %v", err)
		}
		if stderr.Len() > 0 {
			t.Logf("server diagnostics: %s", stderr.String())
		}
	}()
	if got := session.InitializeResult().ProtocolVersion; got != "2026-07-28" {
		t.Fatalf("protocol=%s", got)
	}
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("protocol 2026-07-28; discovered %d tools", len(listed.Tools))
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if r.IsError || r.NeedsInput() {
			data, _ := json.Marshal(r)
			t.Fatalf("%s: %s", name, data)
		}
		t.Logf("%s passed", name)
		return r
	}
	for _, name := range []string{"jetkvm_list_devices", "jetkvm_get_status", "jetkvm_get_capabilities", "jetkvm_get_config"} {
		args := map[string]any{}
		if name == "jetkvm_get_status" || name == "jetkvm_get_capabilities" {
			args["device_id"] = deviceID
		}
		call(name, args)
	}
	caps := []string{"video"}
	inputEnabled := os.Getenv("JETKVM_HIL_INPUT") == "1"
	if inputEnabled {
		caps = append(caps, "input")
	}
	opened := call("jetkvm_open_control", map[string]any{"device_id": deviceID, "requested_capabilities": caps})
	raw, err := json.Marshal(opened.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var handle struct {
		ID         string `json:"control_handle"`
		Generation uint64 `json:"generation"`
	}
	if err := json.Unmarshal(raw, &handle); err != nil || handle.ID == "" {
		t.Fatalf("missing handle: %s, %v", raw, err)
	}
	ref := func() map[string]any {
		return map[string]any{"device_id": deviceID, "control_handle": handle.ID, "expected_generation": handle.Generation}
	}
	closed := false
	defer func() {
		if !closed {
			cleanupCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
			defer done()
			_, _ = session.CallTool(cleanupCtx, &mcp.CallToolParams{Name: "jetkvm_close_control", Arguments: ref()})
		}
	}()
	call("jetkvm_get_control", ref())
	saveImage := func(r *mcp.CallToolResult) {
		t.Helper()
		for _, c := range r.Content {
			if im, ok := c.(*mcp.ImageContent); ok {
				decoded, err := png.Decode(bytes.NewReader(im.Data))
				if err != nil || im.MIMEType != "image/png" {
					t.Fatalf("PNG: %v", err)
				}
				if err := os.WriteFile(output, im.Data, 0600); err != nil {
					t.Fatal(err)
				}
				t.Logf("PNG %dx%d", decoded.Bounds().Dx(), decoded.Bounds().Dy())
				return
			}
		}
		t.Fatal("missing native ImageContent")
	}
	observed := call("jetkvm_observe", ref())
	saveImage(observed)
	if inputEnabled {
		args := ref()
		args["operation_id"] = uuid.NewV7().String()
		args["actions"] = []map[string]any{{"type": "keypress", "keys": []string{"ESC"}}, {"type": "wait", "duration_ms": 100}}
		args["observe_after"] = true
		result := call("jetkvm_run_actions", args)
		saveImage(result)
		raw, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var receipt struct {
			Operation struct {
				Stage     string `json:"stage"`
				Delivery  string `json:"delivery"`
				RetrySafe bool   `json:"retry_safe"`
			} `json:"operation"`
			Batch struct {
				Neutralized bool `json:"neutralized"`
			} `json:"batch"`
		}
		if err := json.Unmarshal(raw, &receipt); err != nil {
			t.Fatal(err)
		}
		if receipt.Operation.Stage != "completed" || receipt.Operation.Delivery != "transport_accepted" || receipt.Operation.RetrySafe || !receipt.Batch.Neutralized {
			t.Fatalf("invalid receipt: %s", raw)
		}
		repeated := call("jetkvm_run_actions", args)
		raw, err = json.Marshal(repeated.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var replay map[string]any
		if err := json.Unmarshal(raw, &replay); err != nil {
			t.Fatal(err)
		}
		if replay["existing"] != true {
			t.Fatal("operation was not deduplicated")
		}
		if _, present := replay["batch"]; present {
			t.Fatal("receipt lookup invented a new batch result")
		}
	}
	saveImage(call("jetkvm_capture_screen", ref()))
	call("jetkvm_close_control", ref())
	closed = true
	if confirmations != 1 {
		t.Fatalf("takeover confirmations=%d, want 1", confirmations)
	}
}
