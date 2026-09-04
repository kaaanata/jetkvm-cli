package app

import (
	"context"
	"fmt"
	"time"

	"github.com/kaaanata/jetkvm-cli/internal/automation"
	"github.com/kaaanata/jetkvm-cli/internal/config"
	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/operation"
)

type sessionPolicy struct {
	idleTimeout      time.Duration
	absoluteLifetime time.Duration
}

// AutomationService is the single CLI/MCP control authority. It preserves one
// underlying automation.Service and applies each device's configured session
// limits before any handle reaches the shared registry.
type AutomationService struct {
	next     *automation.Service
	sessions map[domain.DeviceID]sessionPolicy
}

func newAutomationService(next *automation.Service, cfg config.Config) *AutomationService {
	sessions := make(map[domain.DeviceID]sessionPolicy, len(cfg.Devices))
	for _, device := range cfg.Devices {
		if !device.Exposed {
			continue
		}
		sessions[domain.DeviceID(device.DeviceID)] = sessionPolicy{
			idleTimeout:      device.Session.IdleTimeout.Duration,
			absoluteLifetime: device.Session.AbsoluteLifetime.Duration,
		}
	}
	return &AutomationService{next: next, sessions: sessions}
}

func (s *AutomationService) OpenControl(ctx context.Context, request automation.OpenControlRequest) (control.Handle, error) {
	request, err := s.applySessionPolicy(request)
	if err != nil {
		return control.Handle{}, err
	}
	return s.next.OpenControl(ctx, request)
}

func (s *AutomationService) PrepareOpenControl(request automation.OpenControlRequest) (automation.ConfirmationPlan, error) {
	request, err := s.applySessionPolicy(request)
	if err != nil {
		return automation.ConfirmationPlan{}, err
	}
	return s.next.PrepareOpenControl(request)
}

func (s *AutomationService) applySessionPolicy(request automation.OpenControlRequest) (automation.OpenControlRequest, error) {
	policy, ok := s.sessions[request.DeviceID]
	if !ok {
		return automation.OpenControlRequest{}, domain.ErrDeviceNotExposed
	}
	if request.IdleTimeout == 0 {
		request.IdleTimeout = policy.idleTimeout
	}
	if request.AbsoluteLifetime == 0 {
		request.AbsoluteLifetime = policy.absoluteLifetime
	}
	if request.IdleTimeout > policy.idleTimeout || request.AbsoluteLifetime > policy.absoluteLifetime {
		return automation.OpenControlRequest{}, fmt.Errorf("%w: requested lifetime exceeds device policy", control.ErrInvalidConfig)
	}
	return request, nil
}

func (s *AutomationService) GetControl(ctx context.Context, request automation.ControlRequest) (control.Snapshot, error) {
	return s.next.GetControl(ctx, request)
}

func (s *AutomationService) CloseControl(ctx context.Context, request automation.ControlRequest) (control.Handle, error) {
	return s.next.CloseControl(ctx, request)
}

func (s *AutomationService) RunActions(ctx context.Context, request automation.RunActionsRequest) (automation.RunActionsResult, error) {
	return s.next.RunActions(ctx, request)
}

func (s *AutomationService) Observe(ctx context.Context, request automation.ObserveRequest) (automation.ScreenObservation, error) {
	return s.next.Observe(ctx, request)
}

func (s *AutomationService) PrepareRunActions(request automation.RunActionsRequest) (automation.ConfirmationPlan, error) {
	return s.next.PrepareRunActions(request)
}

func (s *AutomationService) ReleaseInput(ctx context.Context, request automation.ReleaseInputRequest) (operation.Receipt, error) {
	return s.next.ReleaseInput(ctx, request)
}

func (s *AutomationService) GetPowerState(ctx context.Context, request automation.ControlRequest) (automation.PowerState, error) {
	return s.next.GetPowerState(ctx, request)
}

func (s *AutomationService) PowerAction(ctx context.Context, request automation.PowerActionRequest) (operation.Receipt, error) {
	return s.next.PowerAction(ctx, request)
}

func (s *AutomationService) PreparePowerAction(request automation.PowerActionRequest) (automation.ConfirmationPlan, error) {
	return s.next.PreparePowerAction(request)
}

func (s *AutomationService) Drain(ctx context.Context) error {
	return s.next.Drain(ctx)
}
