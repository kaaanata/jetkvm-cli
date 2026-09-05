package terminal

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"github.com/charmbracelet/x/ansi"
	events "github.com/kaaanata/jetkvm-cli/internal/progress"
)

func TestPlainActivityReportsStagesOnly(t *testing.T) {
	var output bytes.Buffer
	a := NewActivity(&output, false)
	for i := range 100 {
		a.Report(events.Event{Stage: "Downloading", Completed: int64(i), Total: 100})
	}
	a.Pause()
	_, _ = a.Write([]byte("diagnostic\n"))
	a.Resume()
	a.Report(events.Event{Stage: "Verifying"})
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a.Report(events.Event{Stage: "late event"})
	if output.String() != "Downloading…\ndiagnostic\nVerifying…\n" {
		t.Fatalf("output=%q", output.String())
	}
}

func TestActivityViewUsesActualByteProgressAndFitsWidth(t *testing.T) {
	for _, total := range []int64{0, 16 << 20} {
		for _, width := range []int{24, 40, 80} {
			a := NewActivity(new(bytes.Buffer), false)
			a.Report(events.Event{Stage: "Downloading archive", Completed: 8 << 20, Total: total})
			a.started = time.Now().Add(-11 * time.Second)
			a.changed = a.started
			m := activityModel{activity: a, width: width, spinner: spinner.New(), bar: progress.New(progress.WithoutPercentage())}
			text := ansi.Strip(m.View().Content)
			if total == 0 && strings.Contains(text, "%") {
				t.Fatal("unknown total has invented percentage")
			}
			if total > 0 && !strings.Contains(text, "50%") {
				t.Fatal("missing measured percentage", text)
			}
			if !strings.Contains(text, "Still waiting") {
				t.Fatal("missing stalled status")
			}
			for line := range strings.SplitSeq(text, "\n") {
				if ansi.StringWidth(line) > width {
					t.Fatalf("overflow %d: %q", width, line)
				}
			}
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			if m.View().Content != "" {
				t.Fatal("progress survived terminal cleanup")
			}
		}
	}
}

func TestActivityOutputFailureIsRetained(t *testing.T) {
	failure := errors.New("stderr unavailable")
	a := NewActivity(errorWriter{failure}, false)
	a.Report(events.Event{Stage: "Checking"})
	if !errors.Is(a.Close(), failure) {
		t.Fatal("output failure lost")
	}
}
