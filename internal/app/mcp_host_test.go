package app

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/onboarding"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connectHost(t *testing.T, h *MCPHost) *mcp.ClientSession {
	t.Helper()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := h.protocol.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "onboarding-test", Version: "test"}, nil)
	cs, err := client.Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestMCPBootstrapAndActivationOnSameConnection(t *testing.T) {
	t.Parallel()
	device := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/device" || r.Method != "GET" {
			t.Errorf("setup performed unexpected device operation %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"authMode":"noPassword","deviceId":"onboarding-fixture","loopbackOnly":false}`)
	}))
	defer device.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	h, err := NewMCPHost(t.Context(), path, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := h.Close(); err != nil {
			t.Error(err)
		}
	})
	cs := connectHost(t, h)
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 3 {
		t.Fatalf("bootstrap tools: %+v", tools.Tools)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "jetkvm_setup" {
			data, _ := json.Marshal(tool.InputSchema)
			if strings.Contains(string(data), "password") || strings.Contains(string(data), "config_path") {
				t.Fatalf("unsafe schema: %s", data)
			}
			var schema map[string]any
			_ = json.Unmarshal(data, &schema)
			if schema["additionalProperties"] != false {
				t.Fatalf("open schema: %s", data)
			}
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("bootstrap created config before approval")
	}
	result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "jetkvm_setup", Arguments: map[string]any{"address": device.URL}})
	if err != nil || result.IsError {
		t.Fatalf("setup=%+v %v", result, err)
	}
	data, _ := json.Marshal(result.StructuredContent)
	var p onboarding.Progress
	if err := json.Unmarshal(data, &p); err != nil || p.URL == "" {
		t.Fatalf("progress %s %v", data, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "POST", p.URL, strings.NewReader(url.Values{"address": {device.URL}, "name": {"My computer"}, "trusted_http": {"yes"}, "approve": {"yes"}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(string(body), "JetKVM is connected") {
		t.Fatalf("browser=%d %s", response.StatusCode, body)
	}
	status, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "jetkvm_setup_status", Arguments: map[string]any{"setup_id": p.ID}})
	if err != nil || status.IsError {
		t.Fatalf("status=%+v %v", status, err)
	}
	data, _ = json.Marshal(status.StructuredContent)
	_ = json.Unmarshal(data, &p)
	if p.Status != "connected" || p.Receipt == nil {
		t.Fatalf("not active: %s", data)
	}
	devices, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "jetkvm_list_devices", Arguments: map[string]any{}})
	if err != nil || devices.IsError {
		t.Fatalf("devices=%+v %v", devices, err)
	}
	data, _ = json.Marshal(devices.StructuredContent)
	if !strings.Contains(string(data), "onboarding-fixture") {
		t.Fatalf("same connection did not activate: %s", data)
	}
	tools, err = cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range tools.Tools {
		if tool.Name == "jetkvm_open_control" {
			found = true
		}
		if tool.Name == "jetkvm_key_press" {
			t.Fatal("input enabled without approval")
		}
	}
	if !found {
		t.Fatal("new device tools not available on existing connection")
	}
	// A CLI-owned write is detected at the next request on this same MCP
	// connection, without a file watcher, restart, or duplicated policy owner.
	settingsService, _ := onboarding.New(onboarding.Options{Path: path})
	settings, err := settingsService.Settings()
	if err != nil {
		t.Fatal(err)
	}
	_, err = settingsService.Update(ctx, onboarding.SettingsPatch{ExpectedRevision: settings.Revision, InputEnabled: new(true), Device: &onboarding.DevicePatch{DeviceID: "onboarding-fixture", InputEnabled: new(true)}})
	if err != nil {
		t.Fatal(err)
	}
	tools, err = cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, tool := range tools.Tools {
		if tool.Name == "jetkvm_key_press" {
			found = true
		}
	}
	if !found {
		t.Fatal("CLI configuration write did not activate in MCP")
	}
	get, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "jetkvm_get_config", Arguments: map[string]any{}})
	if err != nil || get.IsError {
		t.Fatalf("get settings=%+v %v", get, err)
	}
	data, _ = json.Marshal(get.StructuredContent)
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	proposal, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "jetkvm_update_config", Arguments: onboarding.SettingsPatch{ExpectedRevision: settings.Revision, Device: &onboarding.DevicePatch{DeviceID: "onboarding-fixture", InputEnabled: new(false)}}})
	if err != nil || proposal.IsError {
		t.Fatalf("propose=%+v %v", proposal, err)
	}
	data, _ = json.Marshal(proposal.StructuredContent)
	var approval onboarding.Progress
	if err := json.Unmarshal(data, &approval); err != nil {
		t.Fatal(err)
	}
	response, err = http.PostForm(approval.URL, url.Values{"approve": {"yes"}, "input_enabled": {"true"}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if !strings.Contains(string(body), "Settings updated") {
		t.Fatalf("settings approval failed: %s", body)
	}
	tools, err = cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "jetkvm_key_press" {
			t.Fatal("approved input revocation not enforced")
		}
	}
	readback, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "jetkvm_setup_status", Arguments: map[string]any{"setup_id": approval.ID}})
	if err != nil || readback.IsError {
		t.Fatalf("update readback=%+v %v", readback, err)
	}
	data, _ = json.Marshal(readback.StructuredContent)
	if !strings.Contains(string(data), `"status":"updated"`) {
		t.Fatalf("update not retained: %s", data)
	}
}

func TestMCPInvalidConfigDoesNotBecomeBootstrap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"foreign":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if h, err := NewMCPHost(t.Context(), path, "test"); err == nil {
		_ = h.Close()
		t.Fatal("invalid config became writable bootstrap")
	}
}

type settingsTestSession struct{ closed atomic.Int32 }

func (s *settingsTestSession) Close(context.Context) error      { s.closed.Add(1); return nil }
func (s *settingsTestSession) Disconnect(context.Context) error { return nil }

type settingsTestFactory struct{ session *settingsTestSession }

func (f settingsTestFactory) Open(context.Context, domain.DeviceID, uint64, []string) (control.Session, error) {
	return f.session, nil
}

type settingsTestLock struct{}

func (settingsTestLock) Release() error { return nil }

type settingsTestLocker struct{}

func (settingsTestLocker) Acquire(context.Context, domain.DeviceID) (control.Lock, error) {
	return settingsTestLock{}, nil
}

func TestConfigurationActivationDoesNotDrainActiveControls(t *testing.T) {
	device := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"authMode":"noPassword","deviceId":"busy-fixture","loopbackOnly":false}`)
	}))
	defer device.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	service, _ := onboarding.New(onboarding.Options{Path: path})
	if _, err := service.Connect(t.Context(), onboarding.Request{Address: device.URL, AllowHTTP: true}, onboarding.Secret{}); err != nil {
		t.Fatal(err)
	}
	h, err := NewMCPHost(t.Context(), path, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	fake := &settingsTestSession{}
	registry, err := control.NewRegistry(control.Config{Factory: settingsTestFactory{fake}, Locker: settingsTestLocker{}})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Drain(context.Background())
	h.runtime.registry = registry
	handle, err := registry.Open(t.Context(), control.OpenRequest{DeviceID: "busy-fixture", Capabilities: []string{"video"}, Ownership: control.OwnershipOwned})
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	if _, err := h.change(t.Context(), "jetkvm_update_config", func() (onboarding.Receipt, error) { committed = true; return onboarding.Receipt{}, nil }); !errors.Is(err, onboarding.ErrActiveControls) || committed {
		t.Fatalf("active configuration committed: %v %v", err, committed)
	}
	settings, _ := service.Settings()
	if _, err := service.Update(t.Context(), onboarding.SettingsPatch{ExpectedRevision: settings.Revision, Device: &onboarding.DevicePatch{DeviceID: "busy-fixture", TakeoverAllowed: new(false)}}); err != nil {
		t.Fatal(err)
	}
	h.gate.Lock()
	err = h.reconcile(t.Context())
	h.gate.Unlock()
	if !errors.Is(err, onboarding.ErrActiveControls) || fake.closed.Load() != 0 {
		t.Fatalf("external change disconnected control: %v %d", err, fake.closed.Load())
	}
	// A new peer must still be able to initialize and reach cleanup while
	// effects are fenced by a pending configuration revision.
	peer := connectHost(t, h)
	if _, err := peer.ListTools(t.Context(), nil); err == nil {
		t.Fatal("new effects were not fenced while controls remained active")
	}
	if _, err := registry.Close(t.Context(), "busy-fixture", control.Ref{ID: handle.ID, ExpectedGeneration: handle.Generation}); err != nil {
		t.Fatal(err)
	}
	h.gate.Lock()
	err = h.reconcile(t.Context())
	h.gate.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if h.runtime.ConfigRevision == settings.Revision {
		t.Fatal("saved configuration did not activate after cleanup")
	}
}
