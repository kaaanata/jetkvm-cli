package automation

import (
	"context"
	"encoding/json/v2"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/jetkvm"
	"github.com/kaaanata/jetkvm-cli/internal/operation"
	"github.com/kaaanata/jetkvm-cli/internal/video"
)

func TestObservationWakeDefaultsRespectPolicyAndLedger(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		wake, inputPermission, fail bool
	}{
		{"read only", false, true, false}, {"wake", true, true, false}, {"denied", true, false, false}, {"ambiguous", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			permissions := []string{"video"}
			caps := []string{"video"}
			if tc.inputPermission {
				permissions = append(permissions, "input")
				caps = append(caps, "input")
			}
			svc, session, _ := newTestService(t, permissions, "serial-console")
			handle := openTestControl(t, svc, caps)
			reads := 0
			session.observe = func(context.Context) (ScreenObservation, error) {
				reads++
				if session.sendCount() == 0 {
					return ScreenObservation{}, ErrVideoNoSignal
				}
				return ScreenObservation{Data: []byte("png"), Observation: video.Observation{ID: "fresh"}}, nil
			}
			session.failHID = tc.fail
			req := ObserveRequest{ControlRequest: ControlRequest{DeviceID: testDeviceID, Ref: control.Ref{ID: handle.ID, ExpectedGeneration: handle.Generation}}, DisableWake: !tc.wake, WakeOperationID: uuid.NewV7()}
			result, err := svc.Observe(t.Context(), req)
			switch {
			case !tc.wake:
				if !errors.Is(err, ErrVideoNoSignal) || session.sendCount() != 0 || result.Wake != nil {
					t.Fatalf("read-only: %+v %v", result, err)
				}
			case !tc.inputPermission:
				if !errors.Is(err, ErrVideoNoSignal) || session.sendCount() != 0 {
					t.Fatalf("denied: %+v %v", result, err)
				}
			case tc.fail:
				if err == nil || result.Wake == nil || result.Wake.Stage != operation.StageAmbiguous || reads != 1 {
					t.Fatalf("ambiguous: %+v %v reads=%d", result, err, reads)
				}
			default:
				if err != nil || result.Observation.ID != "fresh" || result.Wake == nil || !result.Wake.Batch.Neutralized || result.Wake.Delivery != operation.DeliveryTransportAccepted {
					t.Fatalf("wake: %+v %v", result, err)
				}
			}
		})
	}
}

func TestWakeReceiptSurvivesNoSignalAndDoesNotReplay(t *testing.T) {
	svc, session, _ := newTestService(t, []string{"video", "input"}, "serial-console")
	h := openTestControl(t, svc, []string{"video", "input"})
	session.observe = func(context.Context) (ScreenObservation, error) { return ScreenObservation{}, ErrVideoSleeping }
	req := ObserveRequest{ControlRequest: ControlRequest{DeviceID: testDeviceID, Ref: control.Ref{ID: h.ID, ExpectedGeneration: h.Generation}}, DisableWake: false, WakeOperationID: uuid.NewV7()}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	result, err := svc.Observe(ctx, req)
	if !errors.Is(err, ErrVideoSleeping) || result.Wake == nil || result.Wake.Stage != operation.StageCompleted || !result.Wake.Batch.Neutralized {
		t.Fatalf("partial: %+v %v", result, err)
	}
	sends := session.sendCount()
	ctx2, cancel2 := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel2()
	_, _ = svc.Observe(ctx2, req)
	if session.sendCount() != sends {
		t.Fatal("wake replayed")
	}
}

type readinessProtocol struct {
	fakeProtocolSession
	sleep, signal string
	failure       error
}

func (p *readinessProtocol) CallRPC(_ context.Context, method string, _ any, result any) error {
	if p.failure != nil {
		return p.failure
	}
	if method == "getVideoSleepMode" {
		return json.Unmarshal([]byte(p.sleep), result)
	}
	if method == "getVideoState" {
		return json.Unmarshal([]byte(p.signal), result)
	}
	return errors.New("unexpected RPC")
}
func TestVideoReadinessUsesFirmwareStates(t *testing.T) {
	for _, tc := range []struct {
		name, sleep, signal string
		want                error
	}{
		{"asleep", `{"supported":true,"enabled":true}`, `{}`, ErrVideoSleeping},
		{"no signal", `{"supported":true,"enabled":false}`, `{"ready":false,"error":"no_signal"}`, ErrVideoNoSignal},
		{"ready", `{"supported":true,"enabled":false}`, `{"ready":true}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &readinessProtocol{sleep: tc.sleep, signal: tc.signal}
			s := &sessionAdapter{protocol: p}
			if err := s.videoReadiness(t.Context()); !errors.Is(err, tc.want) {
				t.Fatalf("readiness=%v want=%v", err, tc.want)
			}
			if len(p.hid) != 0 {
				t.Fatal("status read sent HID")
			}
		})
	}
	p := &readinessProtocol{failure: &jetkvm.RPCError{Code: -32601}}
	s := &sessionAdapter{protocol: p}
	if err := s.videoReadiness(t.Context()); err != nil {
		t.Fatal(err)
	}
}
