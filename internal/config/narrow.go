package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

// ProcessConstraints are process-local restrictions. They can never grant a
// capability absent from the deployment configuration.
type ProcessConstraints struct {
	Output            *OutputMode
	ToolsetsAllow     []string
	ToolsetsDeny      []string
	ToolsAllow        []string
	ToolsDeny         []string
	DevicesAllow      []string
	DevicesDeny       []string
	DevicePermissions map[string][]string
	DisableTakeover   []string
}

func (c Config) Narrow(constraints ProcessConstraints) (Config, error) {
	narrowed := c.clone()
	var errs []error
	if constraints.Output != nil {
		narrowed.Output.Default = *constraints.Output
	}
	narrowed.Toolsets, errs = narrowSelection("toolsets", c.Toolsets, constraints.ToolsetsAllow, constraints.ToolsetsDeny, errs)
	narrowed.Tools, errs = narrowSelection("tools", c.Tools, constraints.ToolsAllow, constraints.ToolsDeny, errs)

	if len(constraints.DevicesAllow) > 0 {
		allowed := make(map[string]struct{}, len(constraints.DevicesAllow))
		for _, alias := range constraints.DevicesAllow {
			device, ok := c.Devices[alias]
			if !ok || !device.Exposed {
				errs = append(errs, fmt.Errorf("process device allow %q exceeds deployment exposure", alias))
				continue
			}
			allowed[alias] = struct{}{}
		}
		for alias, device := range narrowed.Devices {
			if _, ok := allowed[alias]; !ok {
				device.Exposed = false
				narrowed.Devices[alias] = device
			}
		}
	}
	for _, alias := range constraints.DevicesDeny {
		device, ok := narrowed.Devices[alias]
		if !ok {
			errs = append(errs, fmt.Errorf("process device deny %q is unknown", alias))
			continue
		}
		device.Exposed = false
		narrowed.Devices[alias] = device
	}
	for alias, permissions := range constraints.DevicePermissions {
		device, ok := narrowed.Devices[alias]
		if !ok {
			errs = append(errs, fmt.Errorf("process permissions target unknown device %q", alias))
			continue
		}
		for _, permission := range permissions {
			if !slices.Contains(device.Permissions, permission) {
				errs = append(errs, fmt.Errorf("process permission %q for device %q exceeds deployment ceiling", permission, alias))
			}
		}
		device.Permissions = slices.Clone(permissions)
		narrowed.Devices[alias] = device
	}
	for _, alias := range constraints.DisableTakeover {
		device, ok := narrowed.Devices[alias]
		if !ok {
			errs = append(errs, fmt.Errorf("process takeover target unknown device %q", alias))
			continue
		}
		device.Takeover.Allowed = false
		narrowed.Devices[alias] = device
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	if err := narrowed.Validate(); err != nil {
		return Config{}, err
	}
	return narrowed, nil
}

func narrowSelection(name string, base Selection, allow, deny []string, errs []error) (Selection, []error) {
	result := Selection{Allow: slices.Clone(base.Allow), Deny: slices.Clone(base.Deny)}
	if len(allow) > 0 {
		for _, value := range allow {
			if (len(base.Allow) > 0 && !slices.Contains(base.Allow, value)) || slices.Contains(base.Deny, value) {
				errs = append(errs, fmt.Errorf("process %s allow %q exceeds deployment ceiling", name, value))
			}
		}
		result.Allow = slices.Clone(allow)
	}
	for _, value := range deny {
		if !slices.Contains(result.Deny, value) {
			result.Deny = append(result.Deny, value)
		}
	}
	return result, errs
}

func (c Config) clone() Config {
	clone := c
	clone.Toolsets.Allow = slices.Clone(c.Toolsets.Allow)
	clone.Toolsets.Deny = slices.Clone(c.Toolsets.Deny)
	clone.Tools.Allow = slices.Clone(c.Tools.Allow)
	clone.Tools.Deny = slices.Clone(c.Tools.Deny)
	clone.Devices = maps.Clone(c.Devices)
	for alias, device := range clone.Devices {
		device.Permissions = slices.Clone(device.Permissions)
		device.Labels = maps.Clone(device.Labels)
		clone.Devices[alias] = device
	}
	return clone
}
