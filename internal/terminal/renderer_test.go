package terminal

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestPlainOutputAndWidth(t *testing.T) {
	for _, scenario := range []struct {
		name string
		tty  bool
		env  string
	}{
		{"pipe", false, ""}, {"no-color", true, "NO_COLOR"}, {"dumb", true, "TERM"}, {"accessible", true, "JETKVM_ACCESSIBLE"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", "")
			t.Setenv("TERM", "xterm-256color")
			t.Setenv("JETKVM_ACCESSIBLE", "")
			t.Setenv("CLICOLOR_FORCE", "1")
			if scenario.env == "TERM" {
				t.Setenv("TERM", "dumb")
			} else if scenario.env != "" {
				t.Setenv(scenario.env, "1")
			}
			for _, width := range []int{24, 59, 60, 80, 120} {
				out := new(bytes.Buffer)
				r := New(out, scenario.tty)
				r.Width = width
				d := Document{Title: "Device status", Sections: []Section{{Headers: []string{"Alias", "Origin"}, Rows: [][]string{{"机架 🖥️", "https://example.invalid/a/very/long/device/path"}, {"\x1b]8;;https://evil.invalid\aLab\x1b]8;;\a\x1b[2J", "source\x00"}}}}}
				if err := r.Write(d); err != nil {
					t.Fatal(err)
				}
				if strings.ContainsAny(out.String(), "\x1b\x00\a\r") {
					t.Fatalf("control sequence escaped plain renderer: %q", out.String())
				}
				if !strings.Contains(out.String(), "机架") || !strings.Contains(out.String(), "Lab") {
					t.Fatalf("display data missing: %q", out.String())
				}
				for line := range strings.SplitSeq(out.String(), "\n") {
					if ansi.StringWidth(line) > width {
						t.Fatalf("width %d overflow: %q", width, line)
					}
				}
			}
		})
	}
}

func TestConfirmAccessible(t *testing.T) {
	for _, test := range []struct {
		input string
		want  bool
	}{{"yes\n", true}, {"y\n", true}, {"no\n", false}, {"\n", false}, {"", false}, {"maybe\n", false}} {
		t.Run(test.input, func(t *testing.T) {
			out := new(bytes.Buffer)
			approved, err := New(out, false).Confirm(t.Context(), strings.NewReader(test.input), "Confirm JetKVM action", "Reset power\nDevice: fixture-device")
			if err != nil || approved != test.want {
				t.Fatalf("approved=%t err=%v", approved, err)
			}
			if !strings.Contains(out.String(), "Device: fixture-device") || !strings.Contains(out.String(), "[y/N]") || strings.Contains(out.String(), "\x1b") {
				t.Fatalf("unsafe prompt: %q", out.String())
			}
		})
	}
}

func TestConfirmFailureAndCancellation(t *testing.T) {
	failure := errors.New("broken stream")
	if ok, err := New(errorWriter{failure}, false).Confirm(t.Context(), strings.NewReader("yes\n"), "Confirm", ""); ok || !errors.Is(err, failure) {
		t.Fatalf("write failure approved=%t err=%v", ok, err)
	}
	if ok, err := New(io.Discard, false).Confirm(t.Context(), errorReader{failure}, "Confirm", ""); ok || !errors.Is(err, failure) {
		t.Fatalf("read failure approved=%t err=%v", ok, err)
	}
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer output.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if ok, err := New(io.Discard, false).Confirm(ctx, input, "Confirm", ""); ok || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation approved=%t err=%v", ok, err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("canceled prompt did not join promptly")
	}
}

type errorWriter struct{ err error }

func (w errorWriter) Write([]byte) (int, error) { return 0, w.err }

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

func TestCharacterDeviceIsNotNecessarilyTerminal(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if IsTerminal(f) {
		t.Fatal("null device is not a terminal")
	}
}

func TestHeaderlessFieldsPreserveValuesAtNarrowWidths(t *testing.T) {
	for _, width := range []int{24, 40, 59, 60, 80} {
		r := New(io.Discard, false)
		r.Width = width
		text := r.Render(Document{Title: "Outcome", Tone: "success", Sections: []Section{Fields("Details", Row("device", "机架-device"), Row("long option label", "012345678901234567890123456789"), Row("injected", "\x1b[2Jvisible"))}})
		if strings.Contains(text, "\x1b") {
			t.Fatalf("escape sequence at width %d", width)
		}
		for line := range strings.SplitSeq(text, "\n") {
			if ansi.StringWidth(line) > width {
				t.Fatalf("width %d overflow: %q", width, line)
			}
		}
		if !strings.Contains(strings.Join(strings.Fields(text), ""), "012345678901234567890123456789") {
			t.Fatalf("value lost: %q", text)
		}
	}
}
