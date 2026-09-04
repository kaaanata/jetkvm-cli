package app

import (
	"context"
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/kaaanata/jetkvm-cli/internal/automation"
	"github.com/kaaanata/jetkvm-cli/internal/config"
	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
)

func TestLoadBuildsSharedHTTPRuntime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/device/status":
			_, _ = w.Write([]byte(`{"isSetup":true}`))
		case "/device":
			_, _ = w.Write([]byte(`{"authMode":"noPassword","deviceId":"device-1","loopbackOnly":false}`))
		case "/cloud/state":
			_, _ = w.Write([]byte(`{"connected":true,"url":"https://api.jetkvm.com","appUrl":"https://app.jetkvm.com"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	directory := t.TempDir()
	cfg := config.Default()
	cfg.State.Path = filepath.Join(directory, "state", "jetkvm.db")
	cfg.Toolsets = config.Selection{Allow: []string{"observe", "video", "input"}}
	cfg.Devices["lab"] = config.DeviceConfig{
		DeviceID:       "device-1",
		Origin:         server.URL,
		Exposed:        true,
		AllowPlainHTTP: true,
		Credentials:    config.CredentialConfig{Provider: config.CredentialNoPassword},
		Permissions:    []string{"observe", "video", "input"},
		Session: config.SessionConfig{
			IdleTimeout:      config.Duration{Duration: 5 * time.Minute},
			AbsoluteLifetime: config.Duration{Duration: 30 * time.Minute},
		},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	runtime, err := Load(t.Context(), path, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("Close() error: %v", err)
		}
	})
	if runtime.MCP == nil || runtime.Policy == nil || runtime.Store == nil || runtime.Automation == nil || runtime.Confirmation == nil {
		t.Fatal("runtime is incomplete")
	}
	status, err := runtime.Devices.GetStatus(t.Context(), domain.DeviceID("device-1"), domain.StatusBasic)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Reachable || status.DeviceID != "device-1" {
		t.Fatalf("status = %+v", status)
	}
	_, err = runtime.Automation.OpenControl(t.Context(), automation.OpenControlRequest{
		DeviceID:     domain.DeviceID("device-1"),
		Capabilities: []string{"video"},
	})
	if !errors.Is(err, domain.ErrTakeoverDisabled) {
		t.Fatalf("OpenControl() error = %v, want ErrTakeoverDisabled", err)
	}
	secretInfo, err := os.Stat(filepath.Join(directory, "state", runtimeSecretName))
	if err != nil {
		t.Fatal(err)
	}
	if got := secretInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("runtime secret mode = %#o, want 0600", got)
	}
}

func TestRuntimeSecretIsStableAndProtected(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	first, err := loadOrCreateRuntimeSecret(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateRuntimeSecret(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first == [sha256.Size]byte{} {
		t.Fatal("runtime secret was not stable and non-zero")
	}
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("state directory mode = %#o, want 0700", got)
	}
	secretInfo, err := os.Stat(filepath.Join(directory, runtimeSecretName))
	if err != nil {
		t.Fatal(err)
	}
	if got := secretInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("runtime secret mode = %#o, want 0600", got)
	}
}

func TestCloseRuntimeDrainsBeforeStoreClose(t *testing.T) {
	var calls []string
	drainer := &recordingDrainer{calls: &calls}
	closer := &recordingCloser{calls: &calls}
	if err := closeRuntime(drainer, closer); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"drain", "close"}) {
		t.Fatalf("close calls = %v", calls)
	}
}

func TestStrictestSessionLimits(t *testing.T) {
	cfg := config.Default()
	cfg.Devices["long-idle"] = config.DeviceConfig{
		Exposed: true,
		Session: config.SessionConfig{
			IdleTimeout:      config.Duration{Duration: 4 * time.Minute},
			AbsoluteLifetime: config.Duration{Duration: 20 * time.Minute},
		},
	}
	cfg.Devices["short-idle"] = config.DeviceConfig{
		Exposed: true,
		Session: config.SessionConfig{
			IdleTimeout:      config.Duration{Duration: 2 * time.Minute},
			AbsoluteLifetime: config.Duration{Duration: 25 * time.Minute},
		},
	}
	idle, absolute := strictestSessionLimits(cfg)
	if idle != 2*time.Minute || absolute != 20*time.Minute {
		t.Fatalf("limits = %v/%v, want 2m/20m", idle, absolute)
	}
}

func TestAutomationServiceAppliesPerDeviceSessionPolicy(t *testing.T) {
	cfg := config.Default()
	cfg.Devices["lab"] = config.DeviceConfig{
		DeviceID: "device-1",
		Exposed:  true,
		Session: config.SessionConfig{
			IdleTimeout:      config.Duration{Duration: 5 * time.Minute},
			AbsoluteLifetime: config.Duration{Duration: 30 * time.Minute},
		},
	}
	service := newAutomationService(nil, cfg)
	_, err := service.OpenControl(t.Context(), automation.OpenControlRequest{
		DeviceID:         domain.DeviceID("device-1"),
		Capabilities:     []string{"video"},
		IdleTimeout:      6 * time.Minute,
		AbsoluteLifetime: 30 * time.Minute,
	})
	if !errors.Is(err, control.ErrInvalidConfig) {
		t.Fatalf("OpenControl() error = %v, want ErrInvalidConfig", err)
	}
	_, err = service.OpenControl(t.Context(), automation.OpenControlRequest{DeviceID: domain.DeviceID("unknown")})
	if !errors.Is(err, domain.ErrDeviceNotExposed) {
		t.Fatalf("unknown-device error = %v, want ErrDeviceNotExposed", err)
	}
}

type recordingDrainer struct {
	calls *[]string
}

func (d *recordingDrainer) Drain(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("drain context has no independent deadline")
	}
	*d.calls = append(*d.calls, "drain")
	return nil
}

type recordingCloser struct {
	calls *[]string
}

func (c *recordingCloser) Close() error {
	*c.calls = append(*c.calls, "close")
	return nil
}
