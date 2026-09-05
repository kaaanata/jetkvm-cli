package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	toolListDevices     = "jetkvm_list_devices"
	toolGetStatus       = "jetkvm_get_status"
	toolGetCapabilities = "jetkvm_get_capabilities"
)

type listDevicesInput struct{}

type listDevicesOutput struct {
	Devices []deviceOutput `json:"devices" jsonschema:"Explicitly exposed JetKVM devices."`
}

type deviceOutput struct {
	DeviceID        string            `json:"device_id" jsonschema:"Stable hardware identity used for authorization and audit."`
	Alias           string            `json:"alias" jsonschema:"Human-readable display alias; never an authorization identity."`
	Origin          string            `json:"origin" jsonschema:"Sanitized device origin without credentials, path, query, or fragment."`
	Permissions     []string          `json:"permissions,omitempty" jsonschema:"Configured permission classes for this device."`
	TakeoverAllowed bool              `json:"takeover_allowed" jsonschema:"Whether policy permits explicit WebRTC session takeover."`
	Labels          map[string]string `json:"labels,omitempty" jsonschema:"Operator-defined labels."`
}

type getStatusInput struct {
	DeviceID domain.DeviceID     `json:"device_id" jsonschema:"Stable JetKVM hardware identity; aliases are not accepted."`
	Detail   domain.StatusDetail `json:"detail,omitempty" jsonschema:"Status detail. The first release supports HTTP-only basic status."`
}

type getStatusOutput struct {
	Status domain.DeviceStatus `json:"status" jsonschema:"HTTP-only, source-attributed device status."`
}

type getCapabilitiesInput struct {
	DeviceID domain.DeviceID `json:"device_id" jsonschema:"Stable JetKVM hardware identity; aliases are not accepted."`
}

type getCapabilitiesOutput struct {
	Capabilities domain.CapabilitySnapshot `json:"capabilities" jsonschema:"Compiled, configured, firmware-supported, and currently-ready capability states."`
}

func (s *Server) registerTools(server *mcp.Server) {
	s.registerSetupTools(server)
	if s.toolAllowed(toolListDevices) {
		mcp.AddTool(server, readOnlyTool(
			toolListDevices,
			"List exposed JetKVM devices",
			"Lists configured devices that are explicitly exposed to MCP without contacting them.",
		), s.listDevices)
	}

	statusInputSchema, err := jsonschema.For[getStatusInput](nil)
	if err != nil {
		panic(fmt.Sprintf("infer %s input schema: %v", toolGetStatus, err))
	}
	statusInputSchema.Properties["device_id"].MinLength = new(1)
	statusInputSchema.Properties["detail"].Enum = []any{domain.StatusBasic}
	statusInputSchema.Properties["detail"].Default = []byte(`"basic"`)
	if s.toolAllowed(toolGetStatus) {
		mcp.AddTool(server, readOnlyTool(
			toolGetStatus,
			"Get basic JetKVM status",
			"Reads source-attributed basic status over HTTP. It never creates or takes over a WebRTC session.",
			withInputSchema(statusInputSchema),
		), s.getStatus)
	}

	capabilitiesInputSchema, err := jsonschema.For[getCapabilitiesInput](nil)
	if err != nil {
		panic(fmt.Sprintf("infer %s input schema: %v", toolGetCapabilities, err))
	}
	capabilitiesInputSchema.Properties["device_id"].MinLength = new(1)
	if s.toolAllowed(toolGetCapabilities) {
		mcp.AddTool(server, readOnlyTool(
			toolGetCapabilities,
			"Get JetKVM capabilities",
			"Returns capability state without creating a WebRTC session. Capability does not imply authorization.",
			withInputSchema(capabilitiesInputSchema),
		), s.getCapabilities)
	}

	s.registerObservationTools(server)
	s.registerControlTools(server)
}

type toolOption func(*mcp.Tool)

func withInputSchema(schema *jsonschema.Schema) toolOption {
	return func(tool *mcp.Tool) {
		tool.InputSchema = schema
	}
}

func readOnlyTool(name, title, description string, options ...toolOption) *mcp.Tool {
	tool := &mcp.Tool{
		Name:        name,
		Title:       title,
		Description: description,
		Annotations: &mcp.ToolAnnotations{
			Title:           title,
			ReadOnlyHint:    true,
			DestructiveHint: new(false),
			IdempotentHint:  true,
			OpenWorldHint:   new(false),
		},
	}
	for _, option := range options {
		option(tool)
	}
	return tool
}

func (s *Server) listDevices(ctx context.Context, _ *mcp.CallToolRequest, _ listDevicesInput) (*mcp.CallToolResult, listDevicesOutput, error) {
	devices, err := s.devices.ListDevices(ctx)
	if err != nil {
		return nil, listDevicesOutput{}, publicDeviceError(err)
	}

	output := listDevicesOutput{Devices: make([]deviceOutput, 0, len(devices))}
	for _, device := range devices {
		view, err := deviceView(device)
		if err != nil {
			return nil, listDevicesOutput{}, err
		}
		output.Devices = append(output.Devices, view)
	}
	return nil, output, nil
}

func (s *Server) getStatus(ctx context.Context, _ *mcp.CallToolRequest, input getStatusInput) (*mcp.CallToolResult, getStatusOutput, error) {
	status, err := s.devices.GetStatus(ctx, input.DeviceID, input.Detail)
	if err != nil {
		return nil, getStatusOutput{}, publicDeviceError(err)
	}
	return nil, getStatusOutput{Status: status}, nil
}

func (s *Server) getCapabilities(ctx context.Context, _ *mcp.CallToolRequest, input getCapabilitiesInput) (*mcp.CallToolResult, getCapabilitiesOutput, error) {
	capabilities, err := s.devices.GetCapabilities(ctx, input.DeviceID, false)
	if err != nil {
		return nil, getCapabilitiesOutput{}, publicDeviceError(err)
	}
	return nil, getCapabilitiesOutput{Capabilities: capabilities}, nil
}

func deviceView(device domain.Device) (deviceOutput, error) {
	origin, err := url.Parse(device.Origin)
	if err != nil || origin.Scheme == "" || origin.Host == "" {
		return deviceOutput{}, fmt.Errorf("device %q has an invalid configured origin", device.ID)
	}

	return deviceOutput{
		DeviceID:        string(device.ID),
		Alias:           device.Alias,
		Origin:          origin.Scheme + "://" + origin.Host,
		Permissions:     device.Permissions,
		TakeoverAllowed: device.TakeoverAllowed,
		Labels:          device.Labels,
	}, nil
}

func publicDeviceError(err error) error {
	switch {
	case errors.Is(err, domain.ErrDeviceNotExposed):
		return domain.ErrDeviceNotExposed
	case errors.Is(err, domain.ErrDeviceIdentityMismatch):
		return domain.ErrDeviceIdentityMismatch
	case errors.Is(err, domain.ErrCapabilityUnavailable):
		return domain.ErrCapabilityUnavailable
	case errors.Is(err, domain.ErrFirmwareUnsupported):
		return domain.ErrFirmwareUnsupported
	default:
		return errors.New("device service request failed")
	}
}
