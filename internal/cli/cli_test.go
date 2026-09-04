package cli

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/kaaanata/jetkvm-cli/internal/buildinfo"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
)

func TestVersionOutputDefaults(t *testing.T) {
	t.Parallel()

	t.Run("non-TTY defaults to JSON", func(t *testing.T) {
		t.Parallel()
		stdout, stderr, app := newTestApp(t, false, &fakeDeviceService{})
		code := app.Execute(t.Context(), []string{"version"})
		if code != ExitOK {
			t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
		}
		var got struct {
			SchemaVersion string         `json:"schema_version"`
			Command       string         `json:"command"`
			Data          buildinfo.Info `json:"data"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatalf("decode output: %v", err)
		}
		if got.SchemaVersion != "v1" || got.Command != "version" || got.Data.Version != "1.2.3" {
			t.Fatalf("unexpected JSON result: %+v", got)
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr = %q", stderr.String())
		}
	})

	t.Run("TTY defaults to text", func(t *testing.T) {
		t.Parallel()
		stdout, stderr, app := newTestApp(t, true, &fakeDeviceService{})
		code := app.Execute(t.Context(), []string{"version"})
		if code != ExitOK {
			t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
		}
		if got, want := stdout.String(), "jetkvm 1.2.3\ncommit: abc123\nbuilt: 2026-09-05T00:00:00Z\nruntime: go1.27.0 darwin/arm64\n"; got != want {
			t.Fatalf("output = %q, want %q", got, want)
		}
	})
}

func TestOutputFlagOverridesTTY(t *testing.T) {
	t.Run("JSON on TTY", func(t *testing.T) {
		t.Parallel()
		stdout, stderr, app := newTestApp(t, true, &fakeDeviceService{})
		code := app.Execute(t.Context(), []string{"--output=json", "version"})
		if code != ExitOK {
			t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
		}
		if !strings.HasPrefix(stdout.String(), "{\n") {
			t.Fatalf("expected JSON, got %q", stdout.String())
		}
	})

	t.Run("text off TTY", func(t *testing.T) {
		t.Parallel()
		stdout, stderr, app := newTestApp(t, false, &fakeDeviceService{})
		code := app.Execute(t.Context(), []string{"--output=text", "version"})
		if code != ExitOK {
			t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
		}
		if !strings.HasPrefix(stdout.String(), "jetkvm 1.2.3\n") {
			t.Fatalf("expected text, got %q", stdout.String())
		}
	})
}

func TestDevicesListJSONAndText(t *testing.T) {
	t.Parallel()
	devices := []domain.Device{{ID: "device-1", Alias: "lab", Origin: "http://example.invalid", Exposed: true}}

	t.Run("JSON", func(t *testing.T) {
		t.Parallel()
		stdout, stderr, app := newTestApp(t, false, &fakeDeviceService{devices: devices})
		if code := app.Execute(t.Context(), []string{"devices", "list"}); code != ExitOK {
			t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), `"device_id": "device-1"`) {
			t.Fatalf("output = %q", stdout.String())
		}
	})

	t.Run("text", func(t *testing.T) {
		t.Parallel()
		stdout, stderr, app := newTestApp(t, true, &fakeDeviceService{devices: devices})
		if code := app.Execute(t.Context(), []string{"devices", "list"}); code != ExitOK {
			t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
		}
		if got, want := stdout.String(), "lab\tdevice-1\thttp://example.invalid\n"; got != want {
			t.Fatalf("output = %q, want %q", got, want)
		}
	})
}

func TestStatusResolvesAliasAndDetail(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 5, 1, 2, 3, 0, time.UTC)
	service := &fakeDeviceService{
		devices: []domain.Device{{ID: "device-1", Alias: "lab", Exposed: true}},
		status:  domain.DeviceStatus{DeviceID: "device-1", Alias: "lab", Observed: now, Reachable: true},
	}
	stdout, stderr, app := newTestApp(t, false, service)
	if code := app.Execute(t.Context(), []string{"status", "lab", "--detail=diagnostic"}); code != ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if service.statusID != "device-1" || service.statusDetail != domain.StatusDiagnostic {
		t.Fatalf("status call = (%q, %q)", service.statusID, service.statusDetail)
	}
	if !strings.Contains(stdout.String(), `"reachable": true`) {
		t.Fatalf("output = %q", stdout.String())
	}
}

func TestCapabilitiesRefresh(t *testing.T) {
	t.Parallel()
	service := &fakeDeviceService{
		devices:      []domain.Device{{ID: "device-1", Alias: "lab", Exposed: true}},
		capabilities: domain.CapabilitySnapshot{DeviceID: "device-1", Alias: "lab", Items: []domain.CapabilityState{}},
	}
	_, stderr, app := newTestApp(t, false, service)
	if code := app.Execute(t.Context(), []string{"capabilities", "device-1", "--refresh"}); code != ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if !service.capabilitiesRefresh {
		t.Fatal("refresh was not forwarded")
	}
}

func TestDoctorUsesAvailableEvidence(t *testing.T) {
	t.Parallel()
	service := &fakeDeviceService{
		devices: []domain.Device{{ID: "device-1", Alias: "lab", Exposed: true}},
		status:  domain.DeviceStatus{DeviceID: "device-1", Alias: "lab", Reachable: true},
		capabilities: domain.CapabilitySnapshot{DeviceID: "device-1", Alias: "lab", Items: []domain.CapabilityState{{
			Name: "video", Compiled: true, Configured: true, FirmwareSupported: true, CurrentlyReady: true,
		}},
		},
	}
	stdout, stderr, app := newTestApp(t, false, service)
	if code := app.Execute(t.Context(), []string{"doctor", "lab"}); code != ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if service.statusDetail != domain.StatusBasic || !service.capabilitiesRefresh {
		t.Fatalf("doctor calls used detail=%q refresh=%t", service.statusDetail, service.capabilitiesRefresh)
	}
	if !strings.Contains(stdout.String(), `"healthy": true`) {
		t.Fatalf("output = %q", stdout.String())
	}
}

func TestFailuresUseStableExitCodesAndStreams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		fail error
		code int
		kind string
	}{
		{name: "invalid output", args: []string{"--output=yaml", "version"}, code: ExitUsage, kind: "invalid_argument"},
		{name: "unknown command", args: []string{"does-not-exist"}, code: ExitUsage, kind: "invalid_argument"},
		{name: "missing argument", args: []string{"status"}, code: ExitUsage, kind: "invalid_argument"},
		{name: "invalid detail", args: []string{"status", "lab", "--detail=everything"}, code: ExitUsage, kind: "invalid_argument"},
		{name: "device absent", args: []string{"status", "missing"}, code: ExitNotFound, kind: "device_not_exposed"},
		{name: "unsupported", args: []string{"status", "lab"}, fail: domain.ErrFirmwareUnsupported, code: ExitUnsupported, kind: "firmware_unsupported"},
		{name: "deadline", args: []string{"status", "lab"}, fail: context.DeadlineExceeded, code: ExitUnavailable, kind: "unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service := &fakeDeviceService{
				devices:   []domain.Device{{ID: "device-1", Alias: "lab", Exposed: true}},
				statusErr: tt.fail,
			}
			stdout, stderr, app := newTestApp(t, false, service)
			if code := app.Execute(t.Context(), tt.args); code != tt.code {
				t.Fatalf("exit code = %d, want %d; stderr = %s", code, tt.code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout should be empty, got %q", stdout.String())
			}
			if !strings.Contains(stderr.String(), `"kind": "`+tt.kind+`"`) {
				t.Fatalf("stderr = %q", stderr.String())
			}
		})
	}
}

func TestMCPServeValidationAndInvocation(t *testing.T) {
	t.Parallel()

	t.Run("HTTP loopback", func(t *testing.T) {
		t.Parallel()
		server := &fakeMCPServer{}
		stdout, stderr, app := newTestApp(t, false, &fakeDeviceService{})
		app.deps.MCP = server
		if code := app.Execute(t.Context(), []string{"mcp", "serve", "--transport=http", "--listen=[::1]:9090"}); code != ExitOK {
			t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
		}
		if server.options.Transport != "http" || server.options.Listen != "[::1]:9090" {
			t.Fatalf("options = %+v", server.options)
		}
		if stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("unexpected output stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
	})

	t.Run("reject non-loopback", func(t *testing.T) {
		t.Parallel()
		server := &fakeMCPServer{}
		stdout, stderr, app := newTestApp(t, false, &fakeDeviceService{})
		app.deps.MCP = server
		if code := app.Execute(t.Context(), []string{"mcp", "serve", "--transport=http", "--listen=0.0.0.0:9090"}); code != ExitUsage {
			t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
		}
		if server.called {
			t.Fatal("MCP server was called for invalid listen address")
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout = %q", stdout.String())
		}
	})
}

func TestRuntimeLoaderUsesConfigOutputAndCloses(t *testing.T) {
	service := &fakeDeviceService{devices: []domain.Device{{ID: "device-1", Alias: "lab", Exposed: true}}}
	stdout, stderr, application := newTestApp(t, true, nil)
	loaded := 0
	closed := 0
	application.configPath = "/configured/path.json"
	application.deps.Loader = RuntimeLoaderFunc(func(_ context.Context, path string) (Runtime, error) {
		loaded++
		if path != "/configured/path.json" {
			t.Fatalf("loader path = %q", path)
		}
		return Runtime{
			Devices:    service,
			MCP:        &fakeMCPServer{},
			OutputMode: "json",
			Close: func() error {
				closed++
				return nil
			},
		}, nil
	})
	if code := application.Execute(t.Context(), []string{"devices", "list"}); code != ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if loaded != 1 || closed != 1 {
		t.Fatalf("runtime lifecycle loaded=%d closed=%d", loaded, closed)
	}
	if !strings.HasPrefix(stdout.String(), "{\n") {
		t.Fatalf("config JSON output was not applied: %q", stdout.String())
	}
}

func TestVersionDoesNotLoadRuntime(t *testing.T) {
	_, stderr, application := newTestApp(t, false, nil)
	application.deps.Loader = RuntimeLoaderFunc(func(context.Context, string) (Runtime, error) {
		t.Fatal("version loaded runtime")
		return Runtime{}, nil
	})
	if code := application.Execute(t.Context(), []string{"version"}); code != ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
}

func newTestApp(t *testing.T, terminal bool, devices domain.DeviceService) (*bytes.Buffer, *bytes.Buffer, *App) {
	t.Helper()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	app := New(Dependencies{
		Devices: devices,
		Version: buildinfo.Info{
			Version: "1.2.3",
			Commit:  "abc123",
			Date:    "2026-09-05T00:00:00Z",
			Go:      "go1.27.0",
			OS:      "darwin",
			Arch:    "arm64",
		},
		Stdin:  strings.NewReader(""),
		Stdout: stdout,
		Stderr: stderr,
		IsTerminal: func(io.Writer) bool {
			return terminal
		},
	})
	return stdout, stderr, app
}

type fakeDeviceService struct {
	devices             []domain.Device
	status              domain.DeviceStatus
	statusErr           error
	statusID            domain.DeviceID
	statusDetail        domain.StatusDetail
	capabilities        domain.CapabilitySnapshot
	capabilitiesErr     error
	capabilitiesID      domain.DeviceID
	capabilitiesRefresh bool
}

func (f *fakeDeviceService) ListDevices(context.Context) ([]domain.Device, error) {
	return f.devices, nil
}

func (f *fakeDeviceService) GetStatus(_ context.Context, id domain.DeviceID, detail domain.StatusDetail) (domain.DeviceStatus, error) {
	f.statusID, f.statusDetail = id, detail
	return f.status, f.statusErr
}

func (f *fakeDeviceService) GetCapabilities(_ context.Context, id domain.DeviceID, refresh bool) (domain.CapabilitySnapshot, error) {
	f.capabilitiesID, f.capabilitiesRefresh = id, refresh
	return f.capabilities, f.capabilitiesErr
}

type fakeMCPServer struct {
	called  bool
	options MCPServeOptions
	err     error
}

func (f *fakeMCPServer) Serve(_ context.Context, options MCPServeOptions) error {
	f.called, f.options = true, options
	return f.err
}

var _ domain.DeviceService = (*fakeDeviceService)(nil)
var _ MCPServer = (*fakeMCPServer)(nil)
