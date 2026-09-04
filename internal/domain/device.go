package domain

import (
	"context"
	"time"
)

// EffectClass is the single server-authoritative classification for tools,
// policies, confirmations, operations, and audit receipts.
type EffectClass string

const (
	EffectObserve      EffectClass = "observe"
	EffectInput        EffectClass = "input"
	EffectPower        EffectClass = "power"
	EffectMedia        EffectClass = "media"
	EffectAdmin        EffectClass = "admin"
	EffectIrreversible EffectClass = "irreversible"
)

// DeviceID is the stable hardware identity reported by a JetKVM device.
type DeviceID string

// Device is an explicitly configured and exposed JetKVM target.
type Device struct {
	ID              DeviceID          `json:"device_id,omitempty"`
	Alias           string            `json:"alias"`
	Origin          string            `json:"origin"`
	Exposed         bool              `json:"exposed"`
	Permissions     []string          `json:"permissions,omitempty"`
	TakeoverAllowed bool              `json:"takeover_allowed,omitzero"`
	Labels          map[string]string `json:"labels,omitempty"`
}

// StatusDetail controls whether a status probe may open a WebRTC session.
type StatusDetail string

const (
	StatusBasic      StatusDetail = "basic"
	StatusStandard   StatusDetail = "standard"
	StatusDiagnostic StatusDetail = "diagnostic"
)

// FieldObservation describes one status field and the authority that produced it.
type FieldObservation struct {
	Value       any       `json:"value,omitempty"`
	Source      string    `json:"source"`
	ObservedAt  time.Time `json:"observed_at"`
	Unavailable string    `json:"unavailable,omitempty"`
}

// DeviceStatus is a point-in-time, source-attributed device observation.
type DeviceStatus struct {
	DeviceID  DeviceID                    `json:"device_id,omitempty"`
	Alias     string                      `json:"alias"`
	Observed  time.Time                   `json:"observed_at"`
	Reachable bool                        `json:"reachable"`
	Fields    map[string]FieldObservation `json:"fields,omitempty"`
}

// CapabilityState separates implementation, policy, firmware and runtime readiness.
type CapabilityState struct {
	Name              string `json:"name"`
	Compiled          bool   `json:"compiled"`
	Configured        bool   `json:"configured"`
	FirmwareSupported bool   `json:"firmware_supported"`
	CurrentlyReady    bool   `json:"currently_ready"`
	Reason            string `json:"reason,omitempty"`
}

// CapabilitySnapshot is the evidence-backed capability view for one device.
type CapabilitySnapshot struct {
	DeviceID DeviceID          `json:"device_id,omitempty"`
	Alias    string            `json:"alias"`
	Observed time.Time         `json:"observed_at"`
	Items    []CapabilityState `json:"capabilities"`
}

// DeviceService is the MCP-facing read-only device contract.
type DeviceService interface {
	ListDevices(context.Context) ([]Device, error)
	GetStatus(context.Context, DeviceID, StatusDetail) (DeviceStatus, error)
	GetCapabilities(context.Context, DeviceID, bool) (CapabilitySnapshot, error)
}
