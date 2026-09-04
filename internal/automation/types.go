package automation

import (
	"time"
	"uuid"

	"github.com/kaaanata/jetkvm-cli/internal/confirmation"
	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/input"
	"github.com/kaaanata/jetkvm-cli/internal/operation"
	"github.com/kaaanata/jetkvm-cli/internal/policy"
	"github.com/kaaanata/jetkvm-cli/internal/video"
)

type OpenControlRequest struct {
	DeviceID         domain.DeviceID
	Capabilities     []string
	Scope            policy.Scope
	Ownership        control.Ownership
	IdleTimeout      time.Duration
	AbsoluteLifetime time.Duration
}

type ControlRequest struct {
	DeviceID domain.DeviceID
	Ref      control.Ref
	Scope    policy.Scope
}

type RunActionsRequest struct {
	DeviceID     domain.DeviceID
	Ref          control.Ref
	Scope        policy.Scope
	OperationID  uuid.UUID
	Batch        input.Batch
	ObserveAfter bool
}

type RunActionsResult struct {
	Operation   operation.Receipt  `json:"operation"`
	Batch       input.BatchReceipt `json:"batch"`
	Existing    bool               `json:"existing,omitzero"`
	Observation *ScreenObservation `json:"observation,omitempty"`
}

type ObserveRequest struct {
	ControlRequest
	Freshness time.Duration
}

// ScreenObservation contains server-owned binding metadata and PNG bytes.
type ScreenObservation struct {
	Observation video.Observation `json:"observation"`
	MIMEType    string            `json:"mime_type"`
	Data        []byte            `json:"-"`
}

type ReleaseInputRequest struct {
	DeviceID    domain.DeviceID
	Ref         control.Ref
	Scope       policy.Scope
	OperationID uuid.UUID
}

// ConfirmationPlan is computed by the same core that executes the operation.
// Adapters may present the request to a user, but may not construct or weaken
// the binding themselves.
type ConfirmationPlan struct {
	Required bool
	Binding  confirmation.Binding
}

type PowerAction string

const (
	PowerPress PowerAction = "press"
	PowerHold  PowerAction = "hold"
	PowerReset PowerAction = "reset"
)

type PowerState struct {
	DeviceID        domain.DeviceID `json:"device_id"`
	ActiveExtension string          `json:"active_extension"`
	PowerLED        bool            `json:"power_led"`
	HDDLED          bool            `json:"hdd_led"`
	ObservedAt      time.Time       `json:"observed_at"`
}

type PowerActionRequest struct {
	DeviceID    domain.DeviceID
	Ref         control.Ref
	Scope       policy.Scope
	OperationID uuid.UUID
	Action      PowerAction
}
