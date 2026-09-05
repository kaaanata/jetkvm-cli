package mcpserver

import (
	"context"
	"errors"

	"github.com/kaaanata/jetkvm-cli/internal/onboarding"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type SetupService interface {
	Begin(onboarding.Draft) (onboarding.Progress, error)
	Status(string) (onboarding.Progress, error)
	Settings() (onboarding.Settings, error)
	BeginUpdate(onboarding.SettingsPatch) (onboarding.Progress, error)
}

type setupStatusInput struct {
	ID string `json:"setup_id"`
}

func (s *Server) registerSetupTools(server *mcp.Server) {
	if s.setup == nil {
		return
	}
	if s.toolAllowed("jetkvm_setup") {
		mcp.AddTool(server, &mcp.Tool{Name: "jetkvm_setup", Title: "Connect a JetKVM", Description: "Start guided device setup. Works before any devices are configured. Suggest only the device address and an optional friendly name; identity and configuration are automatic. Return the local URL to the human for approval and any password. Never request a password in chat. No WebRTC session is created. Check jetkvm_setup_status afterward; the same MCP connection becomes ready automatically.", Annotations: &mcp.ToolAnnotations{DestructiveHint: new(false), OpenWorldHint: new(false)}}, s.beginSetup)
	}
	if s.toolAllowed("jetkvm_setup_status") {
		mcp.AddTool(server, readOnlyTool("jetkvm_setup_status", "Check device setup", "Check the retained setup receipt. A connected result means the configuration is saved and active in this MCP connection. Poll after the user finishes the local form; do not restart MCP."), s.setupStatus)
	}
	if s.toolAllowed("jetkvm_get_config") {
		mcp.AddTool(server, readOnlyTool("jetkvm_get_config", "Read JetKVM settings", "Read credential-free configuration and its revision before proposing a change. Use jetkvm_update_config for updates; never ask the user to edit JSON files."), s.getConfig)
	}
	if s.toolAllowed("jetkvm_update_config") {
		mcp.AddTool(server, &mcp.Tool{Name: "jetkvm_update_config", Title: "Update JetKVM settings", Description: "Propose exact configuration changes against expected_revision from jetkvm_get_config. The human reviews the changes at the returned local URL; check jetkvm_setup_status afterward. Global input enablement and per-device input permission are independent explicit choices. Close active controls first. Stable identities, addresses, credential sources, power permissions, and confirmation requirements cannot be changed by this tool.", Annotations: &mcp.ToolAnnotations{DestructiveHint: new(false), OpenWorldHint: new(false)}}, s.updateConfig)
	}
}

func (s *Server) getConfig(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, onboarding.Settings, error) {
	settings, err := s.setup.Settings()
	if err != nil {
		return nil, onboarding.Settings{}, errors.New("configuration is not ready; use jetkvm_setup to connect your device")
	}
	return nil, settings, nil
}

func (s *Server) updateConfig(_ context.Context, _ *mcp.CallToolRequest, patch onboarding.SettingsPatch) (*mcp.CallToolResult, onboarding.Progress, error) {
	p, err := s.setup.BeginUpdate(patch)
	if err != nil {
		return nil, onboarding.Progress{}, errors.New(onboarding.PublicMessage(err))
	}
	return nil, p, nil
}

func (s *Server) beginSetup(_ context.Context, _ *mcp.CallToolRequest, input onboarding.Draft) (*mcp.CallToolResult, onboarding.Progress, error) {
	p, err := s.setup.Begin(input)
	if err != nil {
		return nil, onboarding.Progress{}, errors.New("device setup could not start; check the address or finish an existing setup")
	}
	return nil, p, nil
}

func (s *Server) setupStatus(_ context.Context, _ *mcp.CallToolRequest, input setupStatusInput) (*mcp.CallToolResult, onboarding.Progress, error) {
	p, err := s.setup.Status(input.ID)
	if err != nil {
		return nil, onboarding.Progress{}, errors.New("setup expired or was not found; start a new setup")
	}
	return nil, p, nil
}

// ClearResources is used only by the application owner behind its dispatch gate.
func ClearResources(server *mcp.Server) {
	server.RemoveResources(devicesResourceURI)
	server.RemoveResourceTemplates(deviceResourceTemplate, statusResourceTemplate, capabilityResourceTemplate)
}
