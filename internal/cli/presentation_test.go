package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/kaaanata/jetkvm-cli/internal/input"
	"github.com/kaaanata/jetkvm-cli/internal/terminal"
	updatecore "github.com/kaaanata/jetkvm-cli/internal/update"
	"github.com/spf13/cobra"
)

func TestJSONBytesIndependentOfPresentation(t *testing.T) {
	const versionJSON = "{\n  \"schema_version\": \"v1\",\n  \"command\": \"version\",\n  \"data\": {\n    \"version\": \"1.2.3\",\n    \"commit\": \"abc123\",\n    \"date\": \"2026-09-05T00:00:00Z\",\n    \"go\": \"go1.27.0\",\n    \"os\": \"darwin\",\n    \"arch\": \"arm64\"\n  }\n}\n"
	for _, tty := range []bool{false, true} {
		for _, noColor := range []string{"", "1"} {
			t.Setenv("NO_COLOR", noColor)
			t.Setenv("TERM", "xterm-256color")
			t.Setenv("CLICOLOR_FORCE", "1")
			out, errOut, app := newTestApp(t, tty, &fakeDeviceService{})
			if code := app.Execute(t.Context(), []string{"version", "--output=json"}); code != ExitOK {
				t.Fatal(errOut.String())
			}
			if out.String() != versionJSON {
				t.Fatalf("JSON bytes changed: %q", out.String())
			}
			if errOut.Len() != 0 {
				t.Fatal("unexpected stderr", errOut.String())
			}
		}
	}
	var baseline []byte
	for _, tty := range []bool{false, true} {
		out, errOut, app := newTestApp(t, tty, &fakeDeviceService{})
		if app.Execute(t.Context(), []string{"--output=json", "--unknown"}) != ExitUsage {
			t.Fatal("wrong usage exit")
		}
		if out.Len() != 0 {
			t.Fatal("error polluted stdout")
		}
		if baseline == nil {
			baseline = bytes.Clone(errOut.Bytes())
		} else if !bytes.Equal(baseline, errOut.Bytes()) {
			t.Fatal("JSON error depends on terminal")
		}
	}
}

func TestHumanViewsKeepReceiptMeaning(t *testing.T) {
	d, err := resultDocument("input.run", runActionsResult{Operation: operationReceiptResult{TerminalClaim: "transport accepted; physical outcome unverified", RetrySafe: false}, Batch: input.BatchReceipt{Status: input.BatchAmbiguous}})
	if err != nil {
		t.Fatal(err)
	}
	var values []string
	for _, s := range d.Sections {
		for _, r := range s.Rows {
			values = append(values, strings.Join(r, ": "))
		}
	}
	for _, want := range []string{"terminal claim: transport accepted; physical outcome unverified", "retry safe: false", "neutralized: false"} {
		if !strings.Contains(strings.Join(values, "\n"), want) {
			t.Fatalf("missing receipt meaning %q", want)
		}
	}
}

func TestReceiptLookupDoesNotInventBatchCleanup(t *testing.T) {
	d, err := resultDocument("input.run", runActionsResult{Existing: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range d.Sections {
		for _, row := range section.Rows {
			if strings.Contains(strings.Join(row, " "), "neutralized") {
				t.Fatal("lookup fabricated cleanup evidence")
			}
		}
	}
}

func TestHelpUsesCobraAndPlainMode(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"input", "drag", "--help"}, {"setup", "--help"}, {"help", "update"}} {
		out, errOut, app := newTestApp(t, false, &fakeDeviceService{})
		if code := app.Execute(t.Context(), args); code != ExitOK {
			t.Fatal(errOut.String())
		}
		if !strings.Contains(out.String(), "Usage") || !strings.Contains(out.String(), "--output") || strings.Contains(out.String(), "\x1b") {
			t.Fatalf("invalid help %q", out.String())
		}
	}
}

func TestMCPOutputBypassesPresentation(t *testing.T) {
	const protocol = "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n"
	out := new(bytes.Buffer)
	app := New(Dependencies{MCP: protocolServer(protocol), Stdout: out, Stderr: io.Discard, IsTerminal: func(io.Writer) bool { return true }})
	if code := app.Execute(t.Context(), []string{"mcp", "serve"}); code != ExitOK || out.String() != protocol {
		t.Fatalf("MCP stream changed: %d %q", code, out.String())
	}
}

type protocolServer string

func (s protocolServer) Serve(_ context.Context, options MCPServeOptions) error {
	_, err := io.WriteString(options.Stdout, string(s))
	return err
}

func TestUpdateOutcomeMeaning(t *testing.T) {
	for _, tc := range []struct {
		name         string
		result       updatecore.Result
		want, absent []string
	}{
		{"current", updatecore.Result{Status: updatecore.StatusUpToDate, CurrentVersion: "1.0.3"}, []string{"Already up to date — JetKVM 1.0.3.\n"}, []string{"verified", "false", "up_to_date", "owner"}},
		{"applied", updatecore.Result{Status: updatecore.StatusApplied, PreviousVersion: "1.0.1", CurrentVersion: "1.0.3", Verified: true, RollbackAvailable: true}, []string{"JetKVM updated", "1.0.1 → 1.0.3", "Artifact verified", "jetkvm update rollback"}, []string{"false"}},
		{"rollback", updatecore.Result{Status: updatecore.StatusRolledBack, PreviousVersion: "1.0.3", CurrentVersion: "1.0.1", Verified: true}, []string{"JetKVM rolled back", "1.0.3 → 1.0.1", "Artifact verified"}, []string{"Signature", "Undo", "rollback available"}},
		{"installer", updatecore.Result{Status: updatecore.StatusActionRequired, CurrentVersion: "1.0.1", Owner: updatecore.OwnerHomebrew, ActionRequired: []string{"brew", "upgrade", "jetkvm"}}, []string{"Update through your installer", "brew upgrade jetkvm", "1.0.1"}, []string{"JetKVM updated", "verified", "false"}},
		{"missing-verification", updatecore.Result{Status: updatecore.StatusApplied, CurrentVersion: "1.0.3"}, []string{"Artifact verification not recorded"}, []string{"Artifact verified", "Undo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := resultDocument("update", tc.result)
			if err != nil {
				t.Fatal(err)
			}
			r := terminal.New(io.Discard, false)
			text := r.Render(d)
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q: %q", want, text)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(text, absent) {
					t.Fatalf("unexpected %q: %q", absent, text)
				}
			}
			if tc.name == "current" && text != tc.want[0] {
				t.Fatalf("no-op should be one sentence: %q", text)
			}
		})
	}
}

func TestGroupedHelpKeepsLiveCommands(t *testing.T) {
	out, _, app := newTestApp(t, false, &fakeDeviceService{})
	root := app.newRootCommand()
	root.AddCommand(&cobra.Command{Use: "future", Short: "Future command", Run: func(*cobra.Command, []string) {}}, &cobra.Command{Use: "cloud", Short: "Cloud account", Run: func(*cobra.Command, []string) {}}, &cobra.Command{Use: "hidden", Hidden: true, Run: func(*cobra.Command, []string) {}})
	if err := app.writeHelp(root, out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"JETKVM", "Get started", "Inspect", "Control", "Integrate", "Maintain", "More commands", "Future command", "Cloud account", "--output", "jetkvm screenshot <device> --file screen.png"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %q", want, text)
		}
	}
	for _, absent := range []string{"Description", "hidden"} {
		if strings.Contains(text, absent) {
			t.Fatalf("unexpected %q: %q", absent, text)
		}
	}
}
