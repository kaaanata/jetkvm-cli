package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/kaaanata/jetkvm-cli/internal/config"
	"github.com/kaaanata/jetkvm-cli/internal/inventory"
)

type DenialReason string

const (
	DeniedUnknownTool           DenialReason = "unknown_tool"
	DeniedDeploymentCeiling     DenialReason = "deployment_ceiling"
	DeniedRequestScope          DenialReason = "request_scope"
	DeniedDeviceRequired        DenialReason = "device_required"
	DeniedDeviceNotExposed      DenialReason = "device_not_exposed"
	DeniedDevicePermission      DenialReason = "device_permission"
	DeniedCapabilityUnavailable DenialReason = "capability_unavailable"
)

type Decision struct {
	Allowed bool
	Reason  DenialReason
	Tool    inventory.ToolDefinition
	Device  DevicePolicy
}

type Scope struct {
	ToolsetsAllow []string
	ToolsetsDeny  []string
	ToolsAllow    []string
	ToolsDeny     []string
}

type Evaluation struct {
	ToolName          string
	DeviceID          string
	Scope             Scope
	Capabilities      map[string]bool
	CheckCapabilities bool
}

type DevicePolicy struct {
	Alias                        string
	DeviceID                     string
	Permissions                  []string
	TakeoverAllowed              bool
	TakeoverRequiresConfirmation bool
}

type Compiled struct {
	catalog       inventory.Catalog
	toolsetsAllow map[string]struct{}
	toolsetsDeny  map[string]struct{}
	toolsAllow    map[string]struct{}
	toolsDeny     map[string]struct{}
	devices       map[string]DevicePolicy
	revision      string
}

func Compile(configuration config.Config, catalog inventory.Catalog) (*Compiled, error) {
	if err := configuration.Validate(); err != nil {
		return nil, err
	}
	compiled := &Compiled{
		catalog:       catalog,
		toolsetsAllow: makeSet(configuration.Toolsets.Allow),
		toolsetsDeny:  makeSet(configuration.Toolsets.Deny),
		toolsAllow:    makeSet(configuration.Tools.Allow),
		toolsDeny:     makeSet(configuration.Tools.Deny),
		devices:       make(map[string]DevicePolicy),
	}
	var errs []error
	for _, name := range append(slices.Clone(configuration.Tools.Allow), configuration.Tools.Deny...) {
		if _, ok := catalog.Get(name); !ok {
			errs = append(errs, fmt.Errorf("configured tool %q is not in the static inventory", name))
		}
	}
	for alias, device := range configuration.Devices {
		if !device.Exposed {
			continue
		}
		if _, duplicate := compiled.devices[device.DeviceID]; duplicate {
			errs = append(errs, fmt.Errorf("duplicate exposed device_id %q", device.DeviceID))
			continue
		}
		compiled.devices[device.DeviceID] = DevicePolicy{
			Alias:                        alias,
			DeviceID:                     device.DeviceID,
			Permissions:                  slices.Clone(device.Permissions),
			TakeoverAllowed:              device.Takeover.Allowed,
			TakeoverRequiresConfirmation: device.Takeover.RequireConfirmation,
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	revision, err := computeRevision(compiled)
	if err != nil {
		return nil, err
	}
	compiled.revision = revision
	return compiled, nil
}

func (c *Compiled) Revision() string {
	return c.revision
}

// Evaluate is the sole decision path for both discovery and execution. An
// empty DeviceID asks whether at least one exposed device may use a scoped tool.
func (c *Compiled) Evaluate(evaluation Evaluation) Decision {
	tool, ok := c.catalog.Get(evaluation.ToolName)
	if !ok {
		return Decision{Reason: DeniedUnknownTool}
	}
	decision := Decision{Tool: tool}
	if !selected(tool.Toolset, tool.Name, c.toolsetsAllow, c.toolsetsDeny, c.toolsAllow, c.toolsDeny) {
		decision.Reason = DeniedDeploymentCeiling
		return decision
	}
	if !selected(tool.Toolset, tool.Name, makeSet(evaluation.Scope.ToolsetsAllow), makeSet(evaluation.Scope.ToolsetsDeny), makeSet(evaluation.Scope.ToolsAllow), makeSet(evaluation.Scope.ToolsDeny)) {
		decision.Reason = DeniedRequestScope
		return decision
	}
	if !tool.DeviceScoped {
		decision.Allowed = true
		return decision
	}
	if evaluation.DeviceID == "" {
		for _, device := range c.devices {
			if slices.Contains(device.Permissions, tool.Toolset) {
				decision.Allowed = true
				return decision
			}
		}
		decision.Reason = DeniedDeviceRequired
		return decision
	}
	device, ok := c.devices[evaluation.DeviceID]
	if !ok {
		decision.Reason = DeniedDeviceNotExposed
		return decision
	}
	decision.Device = device
	if !slices.Contains(device.Permissions, tool.Toolset) {
		decision.Reason = DeniedDevicePermission
		return decision
	}
	if evaluation.CheckCapabilities && tool.Capability != "" && !evaluation.Capabilities[tool.Capability] {
		decision.Reason = DeniedCapabilityUnavailable
		return decision
	}
	decision.Allowed = true
	return decision
}

func (c *Compiled) Tools(scope Scope, deviceID string) []inventory.ToolDefinition {
	var allowed []inventory.ToolDefinition
	for _, tool := range c.catalog.All() {
		if c.Evaluate(Evaluation{ToolName: tool.Name, DeviceID: deviceID, Scope: scope}).Allowed {
			allowed = append(allowed, tool)
		}
	}
	return allowed
}

func selected(toolset, tool string, toolsetsAllow, toolsetsDeny, toolsAllow, toolsDeny map[string]struct{}) bool {
	if _, denied := toolsetsDeny[toolset]; denied {
		return false
	}
	if _, denied := toolsDeny[tool]; denied {
		return false
	}
	if len(toolsetsAllow) > 0 {
		if _, allowed := toolsetsAllow[toolset]; !allowed {
			return false
		}
	}
	if len(toolsAllow) > 0 {
		if _, allowed := toolsAllow[tool]; !allowed {
			return false
		}
	}
	return true
}

func makeSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func computeRevision(compiled *Compiled) (string, error) {
	payload := struct {
		Catalog       []inventory.ToolDefinition
		ToolsetsAllow []string
		ToolsetsDeny  []string
		ToolsAllow    []string
		ToolsDeny     []string
		Devices       []DevicePolicy
	}{
		Catalog:       compiled.catalog.All(),
		ToolsetsAllow: slices.Sorted(maps.Keys(compiled.toolsetsAllow)),
		ToolsetsDeny:  slices.Sorted(maps.Keys(compiled.toolsetsDeny)),
		ToolsAllow:    slices.Sorted(maps.Keys(compiled.toolsAllow)),
		ToolsDeny:     slices.Sorted(maps.Keys(compiled.toolsDeny)),
		Devices: slices.SortedFunc(maps.Values(compiled.devices), func(a, b DevicePolicy) int {
			if a.DeviceID < b.DeviceID {
				return -1
			}
			if a.DeviceID > b.DeviceID {
				return 1
			}
			return 0
		}),
	}
	for index := range payload.Devices {
		slices.Sort(payload.Devices[index].Permissions)
	}
	data, err := json.Marshal(payload, json.Deterministic(true))
	if err != nil {
		return "", fmt.Errorf("marshal policy revision: %w", err)
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
