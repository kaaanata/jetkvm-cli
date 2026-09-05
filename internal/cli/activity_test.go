package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaaanata/jetkvm-cli/internal/progress"
	updatecore "github.com/kaaanata/jetkvm-cli/internal/update"
)

func TestFinalOutputWaitsForControlAndRuntimeCleanup(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "cleanup failure"}[fail], func(t *testing.T) {
			service := newObservingAutomation(t)
			out, errOut, a := newControlTestApp(t, false, service, nil)
			service.onClose = func() error {
				if out.Len() != 0 {
					t.Fatal("result escaped before control cleanup")
				}
				if fail {
					return errors.New("control cleanup failed")
				}
				return nil
			}
			a.runtimeClose = func() error {
				if out.Len() != 0 {
					t.Fatal("result escaped before runtime cleanup")
				}
				if fail {
					return errors.New("runtime cleanup failed")
				}
				return nil
			}
			code := a.Execute(t.Context(), []string{"screenshot", "lab", "--file", filepath.Join(t.TempDir(), "screen.png"), "--output=text"})
			if fail {
				if code == 0 || !strings.Contains(out.String(), "Partial result") || strings.Contains(out.String(), "Screenshot saved") {
					t.Fatalf("false success: %d %s", code, out)
				}
				for _, message := range []string{"control cleanup failed", "runtime cleanup failed"} {
					if !strings.Contains(errOut.String(), message) {
						t.Fatalf("lost error: %s", errOut)
					}
				}
			} else if code != 0 || !strings.Contains(out.String(), "Screenshot saved") {
				t.Fatalf("missing success: %d %s", code, errOut)
			}
		})
	}
}

type progressUpdate struct{ *fakeUpdateService }

func (p progressUpdate) Check(ctx context.Context, r updatecore.Resolution) (updatecore.CheckResult, error) {
	progress.Report(ctx, progress.Event{Stage: "Fixture download", Completed: 1, Total: 2})
	return p.fakeUpdateService.Check(ctx, r)
}

func TestJSONUpdateSuppressesProgressAndConfirmation(t *testing.T) {
	for _, yes := range []bool{false, true} {
		var out, errOut bytes.Buffer
		service := &fakeUpdateService{check: updatecore.CheckResult{Available: true}}
		a := New(Dependencies{Updater: progressUpdate{service}, Stdout: &out, Stderr: &errOut, Stdin: rejectInput{t}, IsTerminal: func(io.Writer) bool { return true }})
		args := []string{"update", "--output=json"}
		if yes {
			args = append(args, "--yes")
		}
		if code := a.Execute(t.Context(), args); code != 0 || service.applyCalls != 1 {
			t.Fatalf("update failed: %d %s", code, &errOut)
		}
		if errOut.Len() != 0 || strings.Contains(out.String(), "Fixture download") {
			t.Fatal("machine output contains progress")
		}
	}
}

type rejectInput struct{ t *testing.T }

func (r rejectInput) Read([]byte) (int, error) {
	r.t.Fatal("unexpected confirmation input read")
	return 0, io.EOF
}

func TestVerboseOnlyAffectsHumanDetails(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		service := newObservingAutomation(t)
		out, errOut, a := newControlTestApp(t, false, service, nil)
		args := []string{"screenshot", "lab", "--file", filepath.Join(t.TempDir(), "screen.png"), "--output=text"}
		if verbose {
			args = append(args, "--verbose")
		}
		if a.Execute(t.Context(), args) != 0 {
			t.Fatal(errOut.String())
		}
		if strings.Contains(out.String(), "obs-owned") != verbose {
			t.Fatalf("detail mode=%v output=%s", verbose, out)
		}
		if !strings.Contains(out.String(), "2 × 2") {
			t.Fatal("missing screenshot dimensions")
		}
	}
}

func TestCancellationDoesNotHideRollbackFailure(t *testing.T) {
	err := &updatecore.Error{Kind: updatecore.ErrRollbackFailed, Message: "restore failed", Cause: &updatecore.Error{Kind: updatecore.ErrReleaseResolution, Cause: context.Canceled}}
	detail := classifyFailure(err)
	if detail.Kind != "rollback_failed" || detail.ExitCode != ExitAmbiguous || detail.Retryable {
		t.Fatalf("unsafe cancellation projection: %+v", detail)
	}
}

func TestCanceledDownloadIsNotPresentedAsRetryableNetworkFailure(t *testing.T) {
	err := &updatecore.Error{Kind: updatecore.ErrReleaseResolution, Cause: context.Canceled}
	detail := classifyFailure(err)
	if detail.Kind != "canceled" || detail.Retryable {
		t.Fatalf("cancellation misclassified: %+v", detail)
	}
}

func TestRollbackIntentDoesNotReadConfirmationInput(t *testing.T) {
	var out, errOut bytes.Buffer
	a := New(Dependencies{Updater: &fakeUpdateService{}, Stdout: &out, Stderr: &errOut, Stdin: rejectInput{t}, IsTerminal: func(io.Writer) bool { return false }})
	if a.Execute(t.Context(), []string{"update", "rollback"}) != 0 {
		t.Fatal(errOut.String())
	}
	if !strings.Contains(out.String(), "rolled_back") {
		t.Fatal("missing rollback receipt")
	}
}

func TestPipedTextUsesStageLinesEvenWithTerminalStderr(t *testing.T) {
	var out, errOut bytes.Buffer
	service := &fakeUpdateService{check: updatecore.CheckResult{Available: true}}
	a := New(Dependencies{Updater: progressUpdate{service}, Stdout: &out, Stderr: &errOut, Stdin: rejectInput{t}, IsTerminal: func(w io.Writer) bool { return w == &errOut }})
	if a.Execute(t.Context(), []string{"update", "--output=text"}) != 0 {
		t.Fatal(errOut.String())
	}
	if !strings.Contains(errOut.String(), "Fixture download") || strings.Contains(errOut.String(), "\x1b") {
		t.Fatalf("invalid plain progress: %q", errOut.String())
	}
}
