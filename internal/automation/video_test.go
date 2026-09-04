package automation

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/input"
	"github.com/kaaanata/jetkvm-cli/internal/operation"
	"github.com/kaaanata/jetkvm-cli/internal/video"
)

func TestPointerBindingResolvedOnlyFromIssuedObservation(t *testing.T) {
	now := time.Now()
	issued := ScreenObservation{Observation: video.Observation{ID: "obs-real", CapturedAt: now, Frame: video.FrameMetadata{Generation: 7, Width: 1920, Height: 1080}}}
	for name, binding := range map[string]input.ObservationBinding{
		"ID only":           {ID: "obs-real", Generation: 7},
		"matching metadata": {ID: "obs-real", Generation: 7, Width: 1920, Height: 1080, CapturedAt: now},
		"forged ID":         {ID: "obs-forged", Generation: 7, Width: 1920, Height: 1080, CapturedAt: now},
		"forged size":       {ID: "obs-real", Generation: 7, Width: 3840},
		"forged time":       {ID: "obs-real", Generation: 7, CapturedAt: now.Add(time.Second)},
		"stale generation":  {ID: "obs-real", Generation: 6},
	} {
		t.Run(name, func(t *testing.T) {
			protocol := &fakeProtocolSession{generation: 7}
			adapter := newTestSessionAdapter(t, protocol, 7)
			adapter.observations = []ScreenObservation{issued}
			_, started, err := adapter.RunActions(t.Context(), input.Batch{Observation: &binding, Actions: []input.Action{{Type: input.ActionClick, Button: input.ButtonLeft, X: 800, Y: 600}}}, func(context.Context) error { return nil })
			valid := name == "ID only" || name == "matching metadata"
			if valid && (err != nil || !started) {
				t.Fatalf("valid binding rejected: %v", err)
			}
			if !valid && (!errors.Is(err, input.ErrObservationStale) || started || len(protocol.hid) != 0) {
				t.Fatalf("forged binding crossed send boundary: %v", err)
			}
		})
	}
}

func TestExpiredIssuedBindingCannotBeRefreshedByCaller(t *testing.T) {
	now := time.Now()
	adapter := &sessionAdapter{observations: []ScreenObservation{{Observation: video.Observation{ID: "old", CapturedAt: now.Add(-input.DefaultLimits().MaxObservationAge - time.Second), Frame: video.FrameMetadata{Generation: 1, Width: 100, Height: 100}}}}}
	_, err := adapter.resolveObservation(&input.ObservationBinding{ID: "old", Generation: 1})
	if !errors.Is(err, input.ErrObservationStale) {
		t.Fatalf("expired binding = %v", err)
	}
}

func TestVideoOnlySessionCloseSendsNoHID(t *testing.T) {
	protocol := &fakeProtocolSession{generation: 1}
	adapter := newTestSessionAdapter(t, protocol, 1)
	adapter.neutralized = true
	if err := adapter.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(protocol.hid) != 0 {
		t.Fatal("video-only cleanup sent input")
	}
}

func TestPostActionObservationFailurePreservesAcceptedReceipt(t *testing.T) {
	for _, cancellation := range []bool{false, true} {
		t.Run(map[bool]string{false: "decode failure", true: "request cancelled"}[cancellation], func(t *testing.T) {
			service, session, _ := newTestService(t, []string{"video", "input"}, "atx-power")
			handle := openTestControl(t, service, []string{"video", "input"})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			session.observe = func(ctx context.Context) (ScreenObservation, error) {
				if cancellation {
					cancel()
					return ScreenObservation{}, context.Cause(ctx)
				}
				return ScreenObservation{}, video.ErrDecodeFailed
			}
			result, err := service.RunActions(ctx, RunActionsRequest{DeviceID: testDeviceID, Ref: control.Ref{ID: handle.ID, ExpectedGeneration: handle.Generation}, OperationID: uuid.NewV7(), ObserveAfter: true, Batch: input.Batch{Actions: []input.Action{{Type: input.ActionKeypress, Keys: []string{"ESC"}}}}})
			if err == nil || result.Operation.Stage != operation.StageCompleted || result.Operation.Delivery != operation.DeliveryTransportAccepted || result.Operation.RetrySafe || !result.Batch.Neutralized {
				t.Fatalf("observation failure changed input terminal receipt: %+v %v", result, err)
			}
		})
	}
}
