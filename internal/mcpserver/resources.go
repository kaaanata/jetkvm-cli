package mcpserver

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"net/url"
	"strings"

	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	devicesResourceURI         = "jetkvm://devices"
	deviceResourceTemplate     = "jetkvm://devices/{device_id}"
	statusResourceTemplate     = "jetkvm://devices/{device_id}/status"
	capabilityResourceTemplate = "jetkvm://devices/{device_id}/capabilities"
	resourceMIMEType           = "application/json"
	devicesResourceTTLMs       = 30_000
	statusResourceTTLMs        = 2_000
	capabilitiesResourceTTLMs  = 300_000
)

func (s *Server) registerResources(server *mcp.Server) {
	if s.toolAllowed(toolListDevices) {
		server.AddResource(&mcp.Resource{
			URI:         devicesResourceURI,
			Name:        "jetkvm_devices",
			Title:       "Exposed JetKVM devices",
			Description: "Configured devices explicitly exposed to this MCP server.",
			MIMEType:    resourceMIMEType,
		}, s.readResource)

		server.AddResourceTemplate(&mcp.ResourceTemplate{
			URITemplate: deviceResourceTemplate,
			Name:        "jetkvm_device",
			Title:       "JetKVM device",
			Description: "Configuration view for one exposed device identified by stable device ID.",
			MIMEType:    resourceMIMEType,
		}, s.readResource)
	}

	if s.toolAllowed(toolGetStatus) {
		server.AddResourceTemplate(&mcp.ResourceTemplate{
			URITemplate: statusResourceTemplate,
			Name:        "jetkvm_device_status",
			Title:       "JetKVM basic status",
			Description: "HTTP-only source-attributed basic status for one exposed device.",
			MIMEType:    resourceMIMEType,
		}, s.readResource)
	}
	if s.toolAllowed(toolGetCapabilities) {
		server.AddResourceTemplate(&mcp.ResourceTemplate{
			URITemplate: capabilityResourceTemplate,
			Name:        "jetkvm_device_capabilities",
			Title:       "JetKVM capabilities",
			Description: "Capability state for one exposed device; capability does not imply authorization.",
			MIMEType:    resourceMIMEType,
		}, s.readResource)
	}
}

func (s *Server) readResource(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	uri := request.Params.URI
	if uri == devicesResourceURI {
		devices, err := s.devices.ListDevices(ctx)
		if err != nil {
			return nil, publicDeviceError(err)
		}
		output := listDevicesOutput{Devices: make([]deviceOutput, 0, len(devices))}
		for _, device := range devices {
			view, err := deviceView(device)
			if err != nil {
				return nil, err
			}
			output.Devices = append(output.Devices, view)
		}
		return jsonResource(uri, devicesResourceTTLMs, output)
	}

	deviceID, kind, err := parseDeviceResourceURI(uri)
	if err != nil {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	switch kind {
	case "device":
		devices, err := s.devices.ListDevices(ctx)
		if err != nil {
			return nil, publicDeviceError(err)
		}
		for _, device := range devices {
			if device.ID == deviceID {
				view, err := deviceView(device)
				if err != nil {
					return nil, err
				}
				return jsonResource(uri, devicesResourceTTLMs, view)
			}
		}
		return nil, mcp.ResourceNotFoundError(uri)
	case "status":
		status, err := s.devices.GetStatus(ctx, deviceID, domain.StatusBasic)
		if err != nil {
			return nil, publicDeviceError(err)
		}
		return jsonResource(uri, statusResourceTTLMs, getStatusOutput{Status: status})
	case "capabilities":
		capabilities, err := s.devices.GetCapabilities(ctx, deviceID, false)
		if err != nil {
			return nil, publicDeviceError(err)
		}
		return jsonResource(uri, capabilitiesResourceTTLMs, getCapabilitiesOutput{Capabilities: capabilities})
	default:
		return nil, mcp.ResourceNotFoundError(uri)
	}
}

func parseDeviceResourceURI(rawURI string) (domain.DeviceID, string, error) {
	parsed, err := url.Parse(rawURI)
	if err != nil || parsed.Scheme != "jetkvm" || parsed.Host != "devices" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", fmt.Errorf("invalid JetKVM resource URI")
	}
	segments := strings.Split(strings.TrimPrefix(parsed.EscapedPath(), "/"), "/")
	if len(segments) < 1 || len(segments) > 2 || segments[0] == "" {
		return "", "", fmt.Errorf("invalid JetKVM resource URI path")
	}
	deviceID, err := url.PathUnescape(segments[0])
	if err != nil || deviceID == "" || strings.Contains(deviceID, "/") {
		return "", "", fmt.Errorf("invalid device ID in resource URI")
	}
	if len(segments) == 1 {
		return domain.DeviceID(deviceID), "device", nil
	}
	if segments[1] != "status" && segments[1] != "capabilities" {
		return "", "", fmt.Errorf("unknown JetKVM resource kind")
	}
	return domain.DeviceID(deviceID), segments[1], nil
}

func jsonResource(uri string, ttlMilliseconds int, value any) (*mcp.ReadResourceResult, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode resource %q: %w", uri, err)
	}
	return &mcp.ReadResourceResult{
		Cacheable: mcp.Cacheable{TTLMs: ttlMilliseconds},
		Contents: []*mcp.ResourceContents{{
			URI:      uri,
			MIMEType: resourceMIMEType,
			Text:     string(data),
		}},
	}, nil
}
