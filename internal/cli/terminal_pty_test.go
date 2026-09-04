//go:build darwin || linux

package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/xpty"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/terminal"
)

// TestTerminalFixture executes only in a subprocess with an actual PTY. The
// device service is a fixture: presentation tests must never send real input.
func TestTerminalFixture(t *testing.T) {
	scenario := os.Getenv("JETKVM_TEST_PTY")
	if scenario == "" {
		return
	}
	if scenario == "confirm" {
		ok, err := terminal.New(os.Stderr, true).Confirm(t.Context(), os.Stdin, "Confirm JetKVM action", "Fixture action only\nDevice: fixture-device")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "choice=%t\n", ok)
		os.Exit(0)
	}
	service := &fakeDeviceService{devices: []domain.Device{{ID: "fixture-device", Alias: "lab", Exposed: true}}, status: domain.DeviceStatus{DeviceID: "fixture-device", Alias: "lab", Reachable: true, Observed: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)}}
	a := New(Dependencies{Devices: service})
	args := map[string][]string{"help": {"input", "--help"}, "status": {"status", "lab"}, "error": {"--unknown"}}[scenario]
	code := a.Execute(t.Context(), args)
	if scenario == "error" && code == ExitUsage {
		code = 0
	}
	os.Exit(code)
}

func TestTerminalPTY(t *testing.T) {
	for _, tc := range []struct {
		name, scenario, env, input, want string
		plain                            bool
	}{
		{"help", "help", "", "", "Commands", false},
		{"status", "status", "", "", "fixture-device", false},
		{"error", "error", "", "", "Error [invalid_argument]", false},
		{"confirm-yes", "confirm", "", "y\r", "choice=true", false},
		{"confirm-default-no", "confirm", "", "\r", "choice=false", false},
		{"no-color-confirm", "confirm", "NO_COLOR=1", "yes\n", "choice=true", true},
		{"dumb-help", "help", "TERM=dumb", "", "Commands", true},
		{"accessible-confirm", "confirm", "JETKVM_ACCESSIBLE=1", "no\n", "choice=false", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			pty, err := xpty.NewUnixPty(80, 30)
			if err != nil {
				t.Fatal(err)
			}
			defer pty.Close()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTerminalFixture$")
			cmd.Env = append(os.Environ(), "TERM=xterm-256color", "NO_COLOR=", "JETKVM_ACCESSIBLE=", "JETKVM_TEST_PTY="+tc.scenario)
			if tc.env != "" {
				cmd.Env = append(cmd.Env, tc.env)
			}
			if err := pty.Start(cmd); err != nil {
				t.Fatal(err)
			}
			// The child owns the slave after Start. Keeping a parent copy open
			// prevents EOF on the master when the child finishes.
			if err := pty.Slave().Close(); err != nil {
				t.Fatal(err)
			}
			chunks := make(chan string, 16)
			go func() {
				defer close(chunks)
				buffer := make([]byte, 4096)
				for {
					n, err := pty.Read(buffer)
					if n > 0 {
						select {
						case chunks <- string(buffer[:n]):
						case <-ctx.Done():
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
			var output strings.Builder
			sent := false
		loop:
			for {
				select {
				case chunk, ok := <-chunks:
					if !ok {
						break loop
					}
					output.WriteString(chunk)
					if tc.input != "" && !sent && strings.Contains(ansi.Strip(output.String()), "Confirm JetKVM action") {
						if _, err := pty.Write([]byte(tc.input)); err != nil {
							t.Fatal(err)
						}
						sent = true
					}
				case <-ctx.Done():
					t.Fatalf("PTY timed out: %q", output.String())
				}
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("child: %v output: %q", err, output.String())
			}
			text := strings.ReplaceAll(ansi.Strip(output.String()), "\r", "")
			if !strings.Contains(text, tc.want) {
				t.Fatalf("missing %q: %q", tc.want, text)
			}
			if strings.Contains(output.String(), "\x1b[?1049h") {
				t.Fatal("unnecessary alternate screen")
			}
			if tc.plain && strings.Contains(output.String(), "\x1b") {
				t.Fatalf("plain PTY contains escapes: %q", output.String())
			}
			if !tc.plain && !strings.Contains(output.String(), "\x1b") {
				t.Fatal("styled PTY did not use the theme")
			}
			t.Log(text)
		})
	}
}
