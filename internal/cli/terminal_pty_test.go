//go:build darwin || linux

package cli

import (
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/xpty"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/input"
	setupcore "github.com/kaaanata/jetkvm-cli/internal/setup"
	"github.com/kaaanata/jetkvm-cli/internal/terminal"
	updatecore "github.com/kaaanata/jetkvm-cli/internal/update"
	"github.com/kaaanata/jetkvm-cli/internal/video"
)

// TestTerminalFixture executes only in a subprocess with an actual PTY. The
// device service is a fixture: presentation tests must never send real input.
func TestTerminalFixture(t *testing.T) {
	scenario := os.Getenv("JETKVM_TEST_PTY")
	if scenario == "" {
		return
	}
	if strings.HasPrefix(scenario, "confirm") {
		ctx := t.Context()
		if scenario == "confirm-timeout" {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 250*time.Millisecond)
			defer cancel()
		}
		ok, err := terminal.New(os.Stderr, true).Confirm(ctx, os.Stdin, "Confirm JetKVM action", "Fixture action only\nDevice: fixture-device")
		if !ok && ((scenario == "confirm-abort" && errors.Is(err, huh.ErrUserAborted)) ||
			(scenario == "confirm-timeout" && errors.Is(err, context.DeadlineExceeded))) {
			fmt.Fprintln(os.Stderr, "aborted=true")
			os.Exit(0)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "choice=%t\n", ok)
		os.Exit(0)
	}
	service := &fakeDeviceService{devices: []domain.Device{{ID: "fixture-device", Alias: "lab", Exposed: true}}, status: domain.DeviceStatus{DeviceID: "fixture-device", Alias: "lab", Reachable: true, Observed: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)}}
	a := New(Dependencies{Devices: service})
	updates := map[string]updatecore.Result{
		"update-current":   {Status: updatecore.StatusUpToDate, CurrentVersion: "1.0.3"},
		"update-applied":   {Status: updatecore.StatusApplied, PreviousVersion: "1.0.1", CurrentVersion: "1.0.3", Verified: true, RollbackAvailable: true},
		"update-rollback":  {Status: updatecore.StatusRolledBack, PreviousVersion: "1.0.3", CurrentVersion: "1.0.1", Verified: true},
		"update-installer": {Status: updatecore.StatusActionRequired, CurrentVersion: "1.0.1", Owner: updatecore.OwnerHomebrew, ActionRequired: []string{"brew", "upgrade", "kaaanata/tap/jetkvm"}},
	}
	if receipt, ok := updates[scenario]; ok {
		if err := a.writeResult("update", receipt); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	views := map[string]any{
		"input":       runActionsResult{Operation: operationReceiptResult{DeviceID: "fixture-device", Action: "input.run", TerminalClaim: "Transport accepted; physical outcome unverified", RetrySafe: false}, Batch: input.BatchReceipt{Neutralized: true}},
		"screenshot":  &screenshotResult{File: "screen.png", Observation: video.Observation{DeviceID: "fixture-device", CapturedAt: service.status.Observed}},
		"setup":       setupcore.Receipt{Target: setupcore.Target{Host: setupcore.HostCodex, Mode: setupcore.ModePlugin, Scope: setupcore.ScopeUser}, Status: setupcore.ReceiptCommitted, OwnedComponents: []string{"jetkvm"}},
		"doctor-view": doctorReport{Healthy: false, Status: service.status, Warnings: []string{"Video capability unavailable in this fixture."}},
	}
	if view, ok := views[scenario]; ok {
		if err := a.writeResult(scenario, view); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	args := map[string][]string{"help": {"input", "--help"}, "root-help": {"--help"}, "status": {"status", "lab"}, "doctor": {"doctor", "lab"}, "error": {"--unknown"}}[scenario]
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
		{"root-help", "root-help", "", "", "Get started", false},
		{"plain-root-help", "root-help", "NO_COLOR=1", "", "Get started", true},
		{"update-current", "update-current", "", "", "Already up to date", false},
		{"plain-update-current", "update-current", "NO_COLOR=1", "", "Already up to date", true},
		{"update-applied", "update-applied", "", "", "Artifact verified", false},
		{"update-rollback", "update-rollback", "", "", "JetKVM rolled back", false},
		{"update-installer", "update-installer", "", "", "Update through your installer", false},
		{"status", "status", "", "", "fixture-device", false},
		{"input", "input", "", "", "physical outcome", false},
		{"screenshot", "screenshot", "", "", "Screenshot saved", false},
		{"setup", "setup", "", "", "Setup completed", false},
		{"doctor", "doctor-view", "", "", "Device needs attention", false},
		{"error", "error", "", "", "Error [invalid_argument]", false},
		{"confirm-yes", "confirm", "", "y\r", "choice=true", false},
		{"confirm-default-no", "confirm", "", "\r", "choice=false", false},
		{"confirm-abort", "confirm-abort", "", "\x03", "aborted=true", false},
		{"confirm-timeout", "confirm-timeout", "", "", "aborted=true", false},
		{"no-color-confirm", "confirm", "NO_COLOR=1", "yes\n", "choice=true", true},
		{"dumb-help", "help", "TERM=dumb", "", "Commands", true},
		{"accessible-confirm", "confirm", "JETKVM_ACCESSIBLE=1", "no\n", "choice=false", true},
	} {
		for _, width := range []int{40, 80} {
			t.Run(fmt.Sprintf("%s-%d", tc.name, width), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				pty, err := xpty.NewUnixPty(width, 60)
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
				if !strings.HasPrefix(tc.scenario, "confirm") {
					for line := range strings.SplitSeq(text, "\n") {
						if ansi.StringWidth(line) > width {
							t.Fatalf("PTY width %d overflow: %q", width, line)
						}
					}
				}
				if directory := os.Getenv("JETKVM_TEST_PTY_EVIDENCE"); directory != "" {
					writePTYEvidence(t, directory, fmt.Sprintf("%s-%d", tc.name, width), output.String(), text, width)
				}
				t.Log(text)
			})
		}
	}
}

// Opt-in evidence comes from the actual child PTY stream, not a UI mockup.
func writePTYEvidence(t *testing.T, directory, name, raw, text string, width int) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	for extension, content := range map[string]string{".ansi": raw, ".txt": text} {
		if err := os.WriteFile(filepath.Join(directory, name+extension), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if strings.HasPrefix(name, "confirm") || strings.Contains(name, "-confirm") {
		return
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(raw, "\r", ""), "\n"), "\n")
	var svg strings.Builder
	fmt.Fprintf(&svg, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d"><rect width="100%%" height="100%%" fill="#16171b"/><g fill="#e5e7ec" font-family="Menlo,monospace" font-size="13">`, width*8+40, len(lines)*20+40)
	for i, line := range lines {
		fmt.Fprintf(&svg, `<text x="20" y="%d" xml:space="preserve">%s</text>`, 30+i*20, svgANSI(line))
	}
	svg.WriteString("</g></svg>")
	if err := os.WriteFile(filepath.Join(directory, name+".svg"), []byte(svg.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The shared theme emits palette SGR colors. Preserve them in the static
// preview, while the .ansi attachment remains the authoritative capture.
func svgANSI(line string) string {
	sgr := regexp.MustCompile(`\x1b\[([0-9;]*)m`)
	colors := map[string]string{"31": "#f49ba5", "32": "#8bd6a8", "33": "#e4c57d", "36": "#69d7cf", "90": "#a1a8b7"}
	color, weight := "#e5e7ec", "normal"
	var result strings.Builder
	start := 0
	write := func(s string) {
		if s != "" {
			fmt.Fprintf(&result, `<tspan fill="%s" font-weight="%s">%s</tspan>`, color, weight, html.EscapeString(ansi.Strip(s)))
		}
	}
	for _, match := range sgr.FindAllStringSubmatchIndex(line, -1) {
		write(line[start:match[0]])
		for code := range strings.SplitSeq(line[match[2]:match[3]], ";") {
			switch code {
			case "", "0":
				color, weight = "#e5e7ec", "normal"
			case "1":
				weight = "bold"
			case "22":
				weight = "normal"
			case "39":
				color = "#e5e7ec"
			default:
				if value, ok := colors[code]; ok {
					color = value
				}
			}
		}
		start = match[1]
	}
	write(line[start:])
	return result.String()
}
