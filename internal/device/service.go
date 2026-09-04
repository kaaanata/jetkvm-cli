// Package device provides the configured, read-only device service.
package device

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/jetkvm"
)

// HTTPClient is the local HTTP observation surface used by DeviceService.
type HTTPClient interface {
	Origin() string
	GetDeviceStatus(context.Context) (jetkvm.DeviceSetup, error)
	GetDevice(context.Context) (jetkvm.LocalDevice, error)
	GetCloudState(context.Context) (jetkvm.CloudState, error)
}

// IdentityVerifier is the single authority for checking a configured identity
// pin. Persistent TOFU or administratively provisioned pins can implement this
// contract without changing the observation service.
type IdentityVerifier interface {
	Verify(context.Context, domain.DeviceID, domain.DeviceID) error
}

// ExactIdentityVerifier requires the observed hardware identity to exactly
// match the configured identity.
type ExactIdentityVerifier struct{}

func (ExactIdentityVerifier) Verify(_ context.Context, expected, observed domain.DeviceID) error {
	if expected == "" || observed == "" || expected != observed {
		return domain.ErrDeviceIdentityMismatch
	}
	return nil
}

// Target binds an explicitly configured device to its protocol client.
type Target struct {
	Device domain.Device
	Client HTTPClient
}

// ServiceConfig configures the read-only device authority.
type ServiceConfig struct {
	Targets          []Target
	IdentityVerifier IdentityVerifier
	Now              func() time.Time
}

// Service implements domain.DeviceService without opening WebRTC.
type Service struct {
	targets  map[domain.DeviceID]Target
	verifier IdentityVerifier
	now      func() time.Time
}

var _ domain.DeviceService = (*Service)(nil)

// NewService validates the complete explicit inventory before publishing it.
func NewService(cfg ServiceConfig) (*Service, error) {
	verifier := cfg.IdentityVerifier
	if verifier == nil {
		verifier = ExactIdentityVerifier{}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	targets := make(map[domain.DeviceID]Target, len(cfg.Targets))
	for _, target := range cfg.Targets {
		if target.Device.ID == "" || target.Device.Alias == "" || target.Device.Origin == "" || target.Client == nil {
			return nil, errors.New("device target requires ID, alias, origin, and HTTP client")
		}
		origin, err := jetkvm.CanonicalOrigin(target.Device.Origin)
		if err != nil {
			return nil, fmt.Errorf("validate device target origin: %w", err)
		}
		if origin != target.Client.Origin() {
			return nil, errors.New("device target origin does not match HTTP client origin")
		}
		target.Device.Origin = origin
		if _, exists := targets[target.Device.ID]; exists {
			return nil, errors.New("duplicate configured device identity")
		}
		targets[target.Device.ID] = target
	}

	return &Service{targets: targets, verifier: verifier, now: now}, nil
}

// ListDevices returns only explicitly exposed devices in deterministic order.
func (s *Service) ListDevices(context.Context) ([]domain.Device, error) {
	devices := make([]domain.Device, 0, len(s.targets))
	for _, target := range s.targets {
		if target.Device.Exposed {
			devices = append(devices, cloneDevice(target.Device))
		}
	}
	slices.SortFunc(devices, func(a, b domain.Device) int {
		if order := cmp.Compare(a.Alias, b.Alias); order != 0 {
			return order
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return devices, nil
}

// GetStatus returns source-attributed HTTP observations. Non-basic detail is
// rejected because this implementation never opens a WebRTC session.
func (s *Service) GetStatus(ctx context.Context, id domain.DeviceID, detail domain.StatusDetail) (domain.DeviceStatus, error) {
	if detail != domain.StatusBasic {
		return domain.DeviceStatus{}, domain.ErrCapabilityUnavailable
	}
	target, err := s.exposedTarget(id)
	if err != nil {
		return domain.DeviceStatus{}, err
	}

	setup, err := target.Client.GetDeviceStatus(ctx)
	if err != nil {
		return domain.DeviceStatus{}, err
	}
	local, err := target.Client.GetDevice(ctx)
	if err != nil {
		return domain.DeviceStatus{}, err
	}
	observedID := domain.DeviceID(local.DeviceID)
	if err := s.verifier.Verify(ctx, target.Device.ID, observedID); err != nil {
		return domain.DeviceStatus{}, fmt.Errorf("verify device identity: %w", err)
	}
	observedAt := s.now()
	fields := map[string]domain.FieldObservation{
		"setup":         field(setup.IsSetup, "/device/status", observedAt),
		"auth_mode":     field(local.AuthMode, "/device", observedAt),
		"loopback_only": field(local.LoopbackOnly, "/device", observedAt),
	}
	cloud, cloudErr := target.Client.GetCloudState(ctx)
	if cloudErr != nil {
		fields["cloud_connected"] = unavailableField("/cloud/state", observedAt, "local cloud state unavailable")
		fields["cloud_url"] = unavailableField("/cloud/state", observedAt, "local cloud state unavailable")
		fields["cloud_app_url"] = unavailableField("/cloud/state", observedAt, "local cloud state unavailable")
	} else {
		fields["cloud_connected"] = field(cloud.Connected, "/cloud/state", observedAt)
		fields["cloud_url"] = field(cloud.URL, "/cloud/state", observedAt)
		fields["cloud_app_url"] = field(cloud.AppURL, "/cloud/state", observedAt)
	}
	return domain.DeviceStatus{
		DeviceID:  observedID,
		Alias:     target.Device.Alias,
		Observed:  observedAt,
		Reachable: true,
		Fields:    fields,
	}, nil
}

// GetCapabilities reports only capabilities established by the HTTP-only
// implementation and explicit device configuration.
func (s *Service) GetCapabilities(ctx context.Context, id domain.DeviceID, runtime bool) (domain.CapabilitySnapshot, error) {
	target, err := s.exposedTarget(id)
	if err != nil {
		return domain.CapabilitySnapshot{}, err
	}

	status, err := s.GetStatus(ctx, id, domain.StatusBasic)
	if err != nil {
		return domain.CapabilitySnapshot{}, err
	}
	items := []domain.CapabilityState{
		{
			Name:              "observe.basic_http",
			Compiled:          true,
			Configured:        true,
			FirmwareSupported: true,
			CurrentlyReady:    !runtime || status.Reachable,
		},
	}
	return domain.CapabilitySnapshot{
		DeviceID: target.Device.ID,
		Alias:    target.Device.Alias,
		Observed: status.Observed,
		Items:    items,
	}, nil
}

func (s *Service) exposedTarget(id domain.DeviceID) (Target, error) {
	target, ok := s.targets[id]
	if !ok || !target.Device.Exposed {
		return Target{}, domain.ErrDeviceNotExposed
	}
	return target, nil
}

func field(value any, source string, observedAt time.Time) domain.FieldObservation {
	return domain.FieldObservation{Value: value, Source: source, ObservedAt: observedAt}
}

func unavailableField(source string, observedAt time.Time, reason string) domain.FieldObservation {
	return domain.FieldObservation{Source: source, ObservedAt: observedAt, Unavailable: reason}
}

func cloneDevice(source domain.Device) domain.Device {
	clone := source
	clone.Permissions = slices.Clone(source.Permissions)
	clone.Labels = maps.Clone(source.Labels)
	return clone
}
