package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kaaanata/jetkvm-cli/internal/automation"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/input"
	"github.com/kaaanata/jetkvm-cli/internal/jetkvm"
	"github.com/kaaanata/jetkvm-cli/internal/video"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type failingObserver struct {
	fakeAutomationService
	failure error
}

func (f *failingObserver) Observe(context.Context, automation.ObserveRequest) (automation.ScreenObservation, error) {
	return automation.ScreenObservation{}, f.failure
}

func TestVideoErrorsSurviveMCPWithoutPrivateDetails(t *testing.T) {
	for _, test := range []struct {
		source, public error
		kind           string
	}{
		{automation.ErrVideoSleeping, automation.ErrVideoSleeping, "video_sleeping"},
		{automation.ErrVideoNoSignal, automation.ErrVideoNoSignal, "video_no_signal"},
		{input.ErrUnknownKey, input.ErrUnknownKey, "invalid_argument"},
		{input.ErrUnsupportedText, input.ErrUnsupportedText, "invalid_argument"},
		{input.ErrNeutralization, input.ErrNeutralization, "unavailable"},
		{jetkvm.ErrSessionClosed, jetkvm.ErrSessionClosed, "unavailable"},
		{jetkvm.ErrSessionReplaced, jetkvm.ErrSessionClosed, "unavailable"},
		{video.ErrVideoUnavailable, video.ErrVideoUnavailable, "unavailable"},
		{video.ErrPipelineClosed, video.ErrVideoUnavailable, "unavailable"},
		{video.ErrDecodeFailed, video.ErrDecodeFailed, "unavailable"},
		{video.ErrDimensionsExceeded, video.ErrDimensionsExceeded, "unavailable"},
		{video.ErrFrameStale, input.ErrObservationStale, "observation_stale"},
		{video.ErrDecoderUnavailable, domain.ErrCapabilityUnavailable, "capability_unavailable"},
		{domain.ErrCapabilityUnavailable, domain.ErrCapabilityUnavailable, "capability_unavailable"},
	} {
		t.Run(test.source.Error(), func(t *testing.T) {
			err := errors.Join(context.DeadlineExceeded, fmt.Errorf("private decoder details: %w", test.source))
			public := publicAutomationError(err)
			if !errors.Is(public, test.public) || !strings.HasPrefix(public.Error(), test.kind+":") || strings.Contains(public.Error(), "private") {
				t.Fatalf("public error=%v", public)
			}
			service := &failingObserver{failure: err}
			adapter, err := New(&fakeDeviceService{}, Options{Automation: service, DecoderAvailable: true})
			if err != nil {
				t.Fatal(err)
			}
			session := connectInMemory(t, adapter.newProtocolServer())
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: toolObserve, Arguments: map[string]any{"device_id": "device-1", "control_handle": "ctl_test", "expected_generation": 7}})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || len(result.Content) != 1 {
				t.Fatalf("result=%+v", result)
			}
			text, ok := result.Content[0].(*mcp.TextContent)
			if !ok || text.Text != public.Error() {
				t.Fatalf("content=%+v", result.Content)
			}
		})
	}
}
