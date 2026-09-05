package automation

import (
	"context"
	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"time"
)

// PowerCapabilities deliberately separates protocol support from physical
// button wiring and host-specific startup semantics.
type PowerCapabilities struct {
	DeviceID        domain.DeviceID `json:"device_id"`
	ActiveExtension string          `json:"active_extension"`
	ObservedAt      time.Time       `json:"observed_at"`
	Paths           []PowerPath     `json:"paths"`
}
type PowerPath struct {
	Path              string `json:"path"`
	ProtocolSupported bool   `json:"protocol_supported"`
	Ready             bool   `json:"ready"`
	HoldDurationsMS   []int  `json:"hold_durations_ms,omitempty"`
	PhysicalButton    bool   `json:"physical_button"`
	Reason            string `json:"reason"`
}

func (s *Service) GetPowerCapabilities(ctx context.Context, request ControlRequest) (PowerCapabilities, error) {
	if _, err := s.authorize("jetkvm_get_control", request.DeviceID, request.Scope, nil, false); err != nil {
		return PowerCapabilities{}, err
	}
	var extension string
	err := s.registry.Execute(ctx, request.DeviceID, request.Ref, "video", func(ctx context.Context, session control.Session) error {
		adapter, err := automationSession(session)
		if err != nil {
			return err
		}
		return adapter.CallRPC(ctx, "getActiveExtension", nil, &extension)
	})
	if err != nil {
		return PowerCapabilities{}, err
	}
	_, powerPolicy := s.authorize("jetkvm_power_action", request.DeviceID, request.Scope, nil, false)
	reason := "requires an active ATX extension wired to the target power-switch contacts"
	if extension == "atx-power" {
		reason = "firmware provides fixed 200ms press and 5000ms hold; physical wiring and host startup behavior require separate verification"
	}
	if powerPolicy != nil {
		reason += "; power policy is disabled"
	}
	return PowerCapabilities{DeviceID: request.DeviceID, ActiveExtension: extension, ObservedAt: s.now(), Paths: []PowerPath{
		{Path: "atx_contact", ProtocolSupported: true, Ready: extension == "atx-power" && powerPolicy == nil, HoldDurationsMS: []int{200, 5000}, PhysicalButton: true, Reason: reason},
		{Path: "usb_system_control", Reason: "current supported JetKVM protocol does not expose Generic Desktop System Control Power; USB wake is a vendor-defined report"},
		{Path: "keyboard_power", Reason: "Keyboard usage 0x66 is distinct from System Control and a physical button; host power/startup behavior is not verified"},
	}}, nil
}
