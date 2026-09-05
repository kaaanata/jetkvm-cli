package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaaanata/jetkvm-cli/internal/config"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/onboarding"
)

func cliSetupFixture(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/device" {
			t.Errorf("unexpected operation %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"authMode":"noPassword","deviceId":"cli-fixture","loopbackOnly":false}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestMissingConfigGuidesHumanThenContinuesDevicesList(t *testing.T) {
	server := cliSetupFixture(t)
	path := filepath.Join(t.TempDir(), "config.json")
	stdout, stderr := new(strings.Builder), new(strings.Builder)
	opened, loaded, closed := 0, 0, 0
	app := New(Dependencies{ConfigPath: path, Stdout: stdout, Stderr: stderr, Stdin: strings.NewReader(""), IsTerminal: func(io.Writer) bool { return true }, InputTerminal: func(io.Reader) bool { return true }, OpenBrowser: func(ctx context.Context, address string) error {
		opened++
		response, err := http.PostForm(address, url.Values{"address": {server.URL}, "name": {"Desk"}, "trusted_http": {"yes"}, "approve": {"yes"}})
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if !strings.Contains(string(body), "JetKVM is connected") {
			t.Fatalf("setup failed: %s", body)
		}
		return nil
	}, Loader: RuntimeLoaderFunc(func(ctx context.Context, path string) (Runtime, error) {
		loaded++
		cfg, err := config.Load(path)
		if err != nil {
			return Runtime{}, err
		}
		device := cfg.Devices["Desk"]
		return Runtime{Devices: &fakeDeviceService{devices: []domain.Device{{ID: domain.DeviceID(device.DeviceID), Alias: "Desk", Origin: device.Origin, Exposed: true}}}, MCP: &fakeMCPServer{}, Close: func() error { closed++; return nil }}, nil
	})})
	if code := app.Execute(t.Context(), []string{"devices", "list"}); code != ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if opened != 1 || loaded != 1 || closed != 1 || !strings.Contains(stdout.String(), "Desk") || !strings.Contains(stderr.String(), "Setup page") {
		t.Fatalf("opened=%d loaded=%d closed=%d stdout=%s stderr=%s", opened, loaded, closed, stdout, stderr)
	}
}

func TestMissingConfigNeverPromptsMachinesOrConsumesPipedInput(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "pipe", true: "json"}[jsonOutput], func(t *testing.T) {
			stdout, stderr := new(strings.Builder), new(strings.Builder)
			app := New(Dependencies{ConfigPath: filepath.Join(t.TempDir(), "config.json"), Stdout: stdout, Stderr: stderr, InputTerminal: func(io.Reader) bool { return jsonOutput }, IsTerminal: func(io.Writer) bool { return true }, OpenBrowser: func(context.Context, string) error { t.Fatal("machine execution opened browser"); return nil }, Loader: RuntimeLoaderFunc(func(context.Context, string) (Runtime, error) { return Runtime{}, config.ErrMissing })})
			args := []string{"devices", "list"}
			if jsonOutput {
				args = append(args, "--output=json")
			}
			if code := app.Execute(t.Context(), args); code != ExitUsage || !strings.Contains(stderr.String(), "configuration_required") || stdout.Len() != 0 {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout, stderr)
			}
		})
	}
}

func TestConfigSetRequiresApprovalAndReturnsJSONReceipt(t *testing.T) {
	server := cliSetupFixture(t)
	path := filepath.Join(t.TempDir(), "config.json")
	service, _ := onboarding.New(onboarding.Options{Path: path})
	if _, err := service.Connect(t.Context(), onboarding.Request{Address: server.URL, Name: "Desk", AllowHTTP: true}, onboarding.Secret{}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	for _, approved := range []bool{false, true} {
		stdout, stderr := new(strings.Builder), new(strings.Builder)
		app := New(Dependencies{ConfigPath: path, Stdout: stdout, Stderr: stderr, IsTerminal: func(io.Writer) bool { return false }, Loader: RuntimeLoaderFunc(func(context.Context, string) (Runtime, error) {
			t.Fatal("config loaded a device runtime")
			return Runtime{}, nil
		})})
		args := []string{"config", "set", "--device=Desk", "--enable-input=true", "--input=true", "--idle-timeout=3m"}
		if approved {
			args = append(args, "--yes")
		}
		code := app.Execute(t.Context(), args)
		if !approved {
			after, _ := os.ReadFile(path)
			if code != ExitAuth || string(before) != string(after) {
				t.Fatalf("unapproved mutation: code=%d %s", code, stderr)
			}
		} else if code != ExitOK || !strings.Contains(stdout.String(), `"command": "config.set"`) || !strings.Contains(stdout.String(), `"status": "updated"`) {
			t.Fatalf("approved update: code=%d stdout=%s stderr=%s", code, stdout, stderr)
		}
	}
}
