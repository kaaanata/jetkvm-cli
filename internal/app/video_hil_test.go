package app

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"uuid"

	"github.com/kaaanata/jetkvm-cli/internal/cli"
	"github.com/kaaanata/jetkvm-cli/internal/config"
	"github.com/kaaanata/jetkvm-cli/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HIL is deliberately opt-in. The temporary policy grants only the explicitly
// requested test surfaces; the operator's persistent configuration is unchanged.
func TestHILVisualCLI(t *testing.T) {
	path, output := os.Getenv("JETKVM_HIL_CONFIG"), os.Getenv("JETKVM_HIL_SCREEN")
	if path == "" || output == "" {
		t.Skip("visual HIL not requested")
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Devices) != 1 {
		t.Fatal("HIL requires exactly one explicit target")
	}
	permissions := []string{"observe", "video"}
	inputEnabled := os.Getenv("JETKVM_HIL_INPUT") == "1"
	if inputEnabled {
		permissions = append(permissions, "input")
	}
	cfg.Toolsets = config.Selection{Allow: permissions}
	cfg.Tools = config.Selection{}
	dir := t.TempDir()
	cfg.State.Path = filepath.Join(dir, "state", "hil.db")
	var alias string
	for name, device := range cfg.Devices {
		alias = name
		device.Permissions = permissions
		device.Takeover.Allowed = true
		device.Takeover.RequireConfirmation = false
		cfg.Devices[name] = device
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{{"screenshot", alias, "--file", output}}
	if inputEnabled {
		commands = append(commands,
			[]string{"input", "move", alias, "--x", "800", "--y", "600", "--file", output},
			[]string{"input", "click", alias, "--x", "800", "--y", "600", "--file", output},
			[]string{"input", "double-click", alias, "--x", "800", "--y", "600", "--file", output},
			[]string{"input", "drag", alias, "--path-json", `[{"x":800,"y":600},{"x":900,"y":650}]`, "--file", output},
			[]string{"input", "scroll", alias, "--delta-y", "1", "--file", output},
			[]string{"input", "run", alias, "--actions-json", `[{"type":"move","x":800,"y":600},{"type":"screenshot"}]`, "--file", output})
	}
	for _, args := range commands {
		t.Run(args[0]+"-"+args[1], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
			defer cancel()
			var stdout, stderr bytes.Buffer
			if binary := os.Getenv("JETKVM_HIL_BINARY"); binary != "" {
				if !filepath.IsAbs(binary) {
					t.Fatal("JETKVM_HIL_BINARY must be an absolute executable path")
				}
				command := exec.CommandContext(ctx, binary, append([]string{"--config", configPath, "--output=json"}, args...)...)
				command.Stdout, command.Stderr = &stdout, &stderr
				if err := command.Run(); err != nil {
					t.Fatalf("released CLI: %v result=%s stderr=%s", err, stdout.String(), stderr.String())
				}
			} else {
				application := cli.New(cli.Dependencies{Stdout: &stdout, Stderr: &stderr, ConfigPath: configPath,
					Loader: cli.RuntimeLoaderFunc(func(ctx context.Context, path string) (cli.Runtime, error) {
						runtime, err := Load(ctx, path, "hil")
						if err != nil {
							return cli.Runtime{}, err
						}
						return cli.Runtime{Devices: runtime.Devices, Automation: runtime.Automation, Releaser: runtime.Automation, MCP: hilUnusedMCP{}, OutputMode: "json", Close: runtime.Close}, nil
					}),
				})
				if code := application.Execute(ctx, args); code != 0 {
					t.Fatalf("CLI exit=%d result=%s stderr=%s", code, stdout.String(), stderr.String())
				}
			}
			var result map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if args[0] == "input" {
				var receipt struct {
					Data struct {
						Operation struct {
							Stage     string `json:"stage"`
							Delivery  string `json:"delivery"`
							RetrySafe bool   `json:"retry_safe"`
						} `json:"operation"`
						Batch struct {
							Neutralized bool `json:"neutralized"`
						} `json:"batch"`
					} `json:"data"`
				}
				if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
					t.Fatal(err)
				}
				if receipt.Data.Operation.Stage != "completed" || receipt.Data.Operation.Delivery != "transport_accepted" || receipt.Data.Operation.RetrySafe || !receipt.Data.Batch.Neutralized {
					t.Fatal("input receipt did not prove accepted, terminally neutralized, non-retryable delivery")
				}
			}
			info, err := os.Stat(output)
			if err != nil || info.Size() == 0 {
				t.Fatal("missing PNG receipt", err)
			}
			t.Logf("CLI accepted; PNG bytes=%d", info.Size())
		})
	}
	if os.Getenv("JETKVM_HIL_MCP") == "1" {
		t.Run("MCP", func(t *testing.T) { testHILMCP(t, configPath, cfg.Devices[alias].DeviceID, inputEnabled) })
	}
}

type hilUnusedMCP struct{}

func (hilUnusedMCP) Serve(context.Context, cli.MCPServeOptions) error {
	return errors.New("MCP is not part of this CLI HIL")
}

type hilBearerTransport struct{}

func (hilBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer isolated-hil-test-token")
	return http.DefaultTransport.RoundTrip(request)
}

func testHILMCP(t *testing.T, path, deviceID string, inputEnabled bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	runtime, err := Load(ctx, path, "hil")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	httpServer, err := runtime.MCP.NewStatelessHTTPServer(mcpserver.HTTPConfig{ListenAddress: "127.0.0.1:0", BearerToken: "isolated-hil-test-token"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpServer.Handler)
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "hil-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: hilBearerTransport{}}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			for _, content := range result.Content {
				if text, ok := content.(*mcp.TextContent); ok {
					t.Log(text.Text)
				}
			}
			t.Fatalf("%s failed", name)
		}
		return result
	}
	capabilities := []string{"video"}
	if inputEnabled {
		capabilities = append(capabilities, "input")
	}
	opened := call("jetkvm_open_control", map[string]any{"device_id": deviceID, "requested_capabilities": capabilities})
	data, err := json.Marshal(opened.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var handle struct {
		ID         string `json:"control_handle"`
		Generation uint64 `json:"generation"`
	}
	if err := json.Unmarshal(data, &handle); err != nil || handle.ID == "" {
		t.Fatal("missing handle", err)
	}
	ref := func() map[string]any {
		return map[string]any{"device_id": deviceID, "control_handle": handle.ID, "expected_generation": handle.Generation}
	}
	observed := call("jetkvm_observe", ref())
	verifyImage := func(result *mcp.CallToolResult) {
		found := false
		for _, content := range result.Content {
			if image, ok := content.(*mcp.ImageContent); ok {
				decoded, err := png.Decode(bytes.NewReader(image.Data))
				if err != nil || image.MIMEType != "image/png" {
					t.Fatal("invalid MCP image", err)
				}
				t.Logf("MCP ImageContent decoded %dx%d", decoded.Bounds().Dx(), decoded.Bounds().Dy())
				found = true
			}
		}
		if !found {
			t.Fatal("MCP omitted native ImageContent")
		}
	}
	verifyImage(observed)
	if inputEnabled {
		data, err := json.Marshal(observed.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var metadata struct {
			Observation struct {
				ID string `json:"observation_id"`
			} `json:"observation"`
		}
		if err := json.Unmarshal(data, &metadata); err != nil || metadata.Observation.ID == "" {
			t.Fatal("missing observation", err)
		}
		args := ref()
		args["operation_id"], args["observation_id"] = uuid.NewV7().String(), metadata.Observation.ID
		args["x"], args["y"], args["observe_after"] = 800, 600, true
		verifyImage(call("jetkvm_pointer_click", args))
	}
	call("jetkvm_close_control", ref())
}
