package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
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
	d, err := resultDocument("input.run", runActionsResult{Operation: operationReceiptResult{TerminalClaim: "transport accepted; physical outcome unverified", RetrySafe: false}})
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
