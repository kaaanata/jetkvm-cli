package app

import (
	"context"

	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/policy"
)

// authorizedDevices makes the compiled policy the execution authority for
// both CLI commands and MCP handlers.
type authorizedDevices struct {
	next   domain.DeviceService
	policy *policy.Compiled
}

func (s *authorizedDevices) ListDevices(ctx context.Context) ([]domain.Device, error) {
	if !s.allowed("jetkvm_list_devices", "") {
		return nil, domain.ErrCapabilityUnavailable
	}
	return s.next.ListDevices(ctx)
}

func (s *authorizedDevices) GetStatus(ctx context.Context, id domain.DeviceID, detail domain.StatusDetail) (domain.DeviceStatus, error) {
	if !s.allowed("jetkvm_get_status", id) {
		return domain.DeviceStatus{}, domain.ErrCapabilityUnavailable
	}
	return s.next.GetStatus(ctx, id, detail)
}

func (s *authorizedDevices) GetCapabilities(ctx context.Context, id domain.DeviceID, refresh bool) (domain.CapabilitySnapshot, error) {
	if !s.allowed("jetkvm_get_capabilities", id) {
		return domain.CapabilitySnapshot{}, domain.ErrCapabilityUnavailable
	}
	return s.next.GetCapabilities(ctx, id, refresh)
}

func (s *authorizedDevices) allowed(tool string, deviceID domain.DeviceID) bool {
	return s.policy.Evaluate(policy.Evaluation{
		ToolName: tool,
		DeviceID: string(deviceID),
	}).Allowed
}

var _ domain.DeviceService = (*authorizedDevices)(nil)
