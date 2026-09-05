package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kaaanata/jetkvm-cli/internal/automation"
	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/policy"
	"github.com/kaaanata/jetkvm-cli/internal/video"
)

type observingAutomation struct {
	*fakeAutomation
	screen   automation.ScreenObservation
	observed automation.ObserveRequest
	closed   automation.ControlRequest
	onClose  func() error
}

func (f *observingAutomation) Observe(_ context.Context, request automation.ObserveRequest) (automation.ScreenObservation, error) {
	f.observed = request
	return f.screen, nil
}

func (f *observingAutomation) CloseControl(ctx context.Context, request automation.ControlRequest) (control.Handle, error) {
	f.closed = request
	if f.onClose != nil {
		if err := f.onClose(); err != nil {
			return control.Handle{}, err
		}
	}
	return f.fakeAutomation.CloseControl(ctx, request)
}

func (f *observingAutomation) RunActions(ctx context.Context, request automation.RunActionsRequest) (automation.RunActionsResult, error) {
	result, err := f.fakeAutomation.RunActions(ctx, request)
	if request.ObserveAfter {
		result.Observation = &f.screen
	}
	return result, err
}

func newObservingAutomation(t *testing.T) *observingAutomation {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return &observingAutomation{fakeAutomation: newFakeAutomation(), screen: automation.ScreenObservation{
		MIMEType: "image/png", Data: data.Bytes(), Observation: video.Observation{ID: "obs-owned", DeviceID: "device-1", CapturedAt: time.Now(), Frame: video.FrameMetadata{Generation: 7, Width: 2, Height: 2}},
	}}
}

func TestScreenshotFileAndAlias(t *testing.T) {
	for _, name := range []string{"screenshot", "observe"} {
		t.Run(name, func(t *testing.T) {
			service := newObservingAutomation(t)
			stdout, stderr, app := newControlTestApp(t, false, service, nil)
			file := filepath.Join(t.TempDir(), "screen.png")
			if code := app.Execute(t.Context(), []string{name, "lab", "--file", file}); code != ExitOK {
				t.Fatalf("exit=%d: %s", code, stderr)
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, service.screen.Data) {
				t.Fatal("PNG changed")
			}
			if _, err := png.Decode(bytes.NewReader(data)); err != nil {
				t.Fatal(err)
			}
			if service.observed.Ref != service.closed.Ref || service.closed.Ref.ID != "ctl_test" {
				t.Fatal("capture and cleanup use different controls")
			}
			if !slices.Equal(service.open.Capabilities, []string{"video", "input"}) {
				t.Fatalf("default screenshot requested capabilities %v", service.open.Capabilities)
			}
			if strings.Contains(stdout.String(), base64.StdEncoding.EncodeToString(service.screen.Data)) || !strings.Contains(stdout.String(), "obs-owned") {
				t.Fatalf("output=%s", stdout)
			}
		})
	}
}

func TestPointerCapturesWithinSameEphemeralControl(t *testing.T) {
	for _, args := range [][]string{{"move", "--x=1", "--y=1"}, {"click", "--x=1", "--y=1"}, {"double-click", "--x=1", "--y=1"}, {"drag", `--path-json=[{"x":0,"y":0},{"x":1,"y":1}]`}} {
		t.Run(args[0], func(t *testing.T) {
			service := newObservingAutomation(t)
			_, stderr, app := newControlTestApp(t, false, service, nil)
			command := append([]string{"input", args[0], "lab"}, args[1:]...)
			if code := app.Execute(t.Context(), command); code != ExitOK {
				t.Fatalf("exit=%d: %s", code, stderr)
			}
			binding := service.actions.Batch.Observation
			if binding == nil || binding.ID != "obs-owned" || binding.Generation != 7 || binding.Width != 2 || binding.CapturedAt != service.screen.Observation.CapturedAt {
				t.Fatalf("binding=%+v", binding)
			}
			if service.observed.Ref != service.actions.Ref || !slices.Contains(service.open.Capabilities, "video") {
				t.Fatal("observation was not captured on the action control")
			}
		})
	}
}

func TestRunObserveAfterWritesPNG(t *testing.T) {
	service := newObservingAutomation(t)
	stdout, stderr, app := newControlTestApp(t, false, service, nil)
	file := filepath.Join(t.TempDir(), "after.png")
	if code := app.Execute(t.Context(), []string{"input", "run", "lab", `--actions-json=[{"type":"screenshot"}]`, "--observe-after", "--file", file}); code != ExitOK {
		t.Fatalf("exit=%d: %s", code, stderr)
	}
	data, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(data, service.screen.Data) || !service.actions.ObserveAfter || !strings.Contains(stdout.String(), "obs-owned") {
		t.Fatalf("capture failed: %v %s", err, stdout)
	}
}

func TestEphemeralControlRejectsForeignObservation(t *testing.T) {
	service := newObservingAutomation(t)
	_, _, app := newControlTestApp(t, false, service, nil)
	if code := app.Execute(t.Context(), []string{"input", "click", "lab", "--x=1", "--y=1", "--observation-id=foreign"}); code == ExitOK || service.runCalls != 0 || service.open.DeviceID != "" {
		t.Fatal("foreign observation was rebound")
	}
}

func TestPostActionBase64RequiresExplicitFlag(t *testing.T) {
	service := newObservingAutomation(t)
	stdout, stderr, app := newControlTestApp(t, false, service, nil)
	if code := app.Execute(t.Context(), []string{"input", "key", "lab", "A", "--observe-after", "--image-base64"}); code != ExitOK {
		t.Fatalf("exit=%d: %s", code, stderr)
	}
	if !strings.Contains(stdout.String(), base64.StdEncoding.EncodeToString(service.screen.Data)) || !service.actions.ObserveAfter {
		t.Fatalf("missing explicit PNG payload: %s", stdout)
	}
	service = newObservingAutomation(t)
	_, _, app = newControlTestApp(t, false, service, nil)
	if code := app.Execute(t.Context(), []string{"input", "key", "lab", "A", "--observe-after"}); code == ExitOK || service.open.DeviceID != "" {
		t.Fatal("missing screenshot destination was not rejected before opening control")
	}
}

func (f *observingAutomation) CanWake(domain.DeviceID, policy.Scope) bool { return true }

func TestScreenshotNoWakeKeepsVideoOnly(t *testing.T) {
	service := newObservingAutomation(t)
	_, stderr, app := newControlTestApp(t, false, service, nil)
	file := filepath.Join(t.TempDir(), "screen.png")
	if code := app.Execute(t.Context(), []string{"screenshot", "lab", "--no-wake", "--file", file}); code != ExitOK {
		t.Fatal(stderr.String())
	}
	if !service.observed.DisableWake || !slices.Equal(service.open.Capabilities, []string{"video"}) {
		t.Fatal("no-wake did not preserve video-only capture")
	}
}
