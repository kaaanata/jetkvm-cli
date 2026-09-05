package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaaanata/jetkvm-cli/internal/config"
	"github.com/kaaanata/jetkvm-cli/internal/onboarding"
)

func TestConfigSetConfirmationFlagAndReadback(t *testing.T) {
	server := cliSetupFixture(t)
	path := filepath.Join(t.TempDir(), "config.json")
	service, err := onboarding.New(onboarding.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Connect(t.Context(), onboarding.Request{Address: server.URL, Name: "Desk", AllowHTTP: true}, onboarding.Secret{}); err != nil {
		t.Fatal(err)
	}
	for _, required := range []bool{true, false} {
		stdout, stderr := new(strings.Builder), new(strings.Builder)
		app := New(Dependencies{ConfigPath: path, Stdout: stdout, Stderr: stderr, IsTerminal: func(io.Writer) bool { return false }})
		if code := app.Execute(t.Context(), []string{"config", "set", fmt.Sprintf("--require-confirmation=%t", required), "--yes"}); code != ExitOK {
			t.Fatalf("set: code=%d stderr=%s", code, stderr)
		}
		cfg, err := config.Load(path)
		if err != nil || cfg.Confirmation.Required != required {
			t.Fatalf("persisted confirmation: %+v, %v", cfg.Confirmation, err)
		}
		stdout.Reset()
		app = New(Dependencies{ConfigPath: path, Stdout: stdout, Stderr: stderr, IsTerminal: func(io.Writer) bool { return false }})
		if code := app.Execute(t.Context(), []string{"config", "show"}); code != ExitOK || !strings.Contains(stdout.String(), fmt.Sprintf(`"confirmation_required": %t`, required)) {
			t.Fatalf("show: code=%d stdout=%s stderr=%s", code, stdout, stderr)
		}
	}
}
