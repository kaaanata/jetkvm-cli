// Package mcpserver adapts the JetKVM domain services to the Model Context
// Protocol. It owns MCP schemas and transports, but no device lifecycle state.
package mcpserver

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"time"

	"github.com/kaaanata/jetkvm-cli/internal/automation"
	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/operation"
	"github.com/kaaanata/jetkvm-cli/internal/policy"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultServerName    = "jetkvm-mcp"
	defaultServerVersion = "dev"
)

// Options identifies this MCP server implementation to clients.
type Options struct {
	Name               string
	Version            string
	AllowedTools       map[string]bool
	Automation         AutomationService
	ConfirmationIssuer ConfirmationIssuer
	PolicyRevision     string
	Scope              policy.Scope
	DecoderAvailable   bool
}

// AutomationService is the sole control-plane dependency of MCP handlers.
// The concrete automation.Service retains policy, actor, session, input and
// operation-ledger authority.
type AutomationService interface {
	PrepareOpenControl(automation.OpenControlRequest) (automation.ConfirmationPlan, error)
	OpenControl(context.Context, automation.OpenControlRequest) (control.Handle, error)
	GetControl(context.Context, automation.ControlRequest) (control.Snapshot, error)
	CloseControl(context.Context, automation.ControlRequest) (control.Handle, error)
	RunActions(context.Context, automation.RunActionsRequest) (automation.RunActionsResult, error)
	PrepareRunActions(automation.RunActionsRequest) (automation.ConfirmationPlan, error)
	GetPowerState(context.Context, automation.ControlRequest) (automation.PowerState, error)
	PowerAction(context.Context, automation.PowerActionRequest) (operation.Receipt, error)
	PreparePowerAction(automation.PowerActionRequest) (automation.ConfirmationPlan, error)
}

// Server is the stateless MCP adapter over a DeviceService.
//
// Each transport gets an MCP server instance with the same immutable feature
// inventory. Stateful device ownership remains behind DeviceService.
type Server struct {
	devices      domain.DeviceService
	name         string
	version      string
	schemaCache  *mcp.SchemaCache
	allowedTools map[string]bool
	automation   AutomationService
	confirm      ConfirmationIssuer
	policy       string
	scope        policy.Scope
	decoder      bool
}

// New constructs an MCP adapter. It performs all configuration validation that
// is independent of a concrete transport.
func New(devices domain.DeviceService, options Options) (*Server, error) {
	if devices == nil {
		return nil, errors.New("mcpserver: nil device service")
	}

	return &Server{
		devices:      devices,
		name:         cmp.Or(options.Name, defaultServerName),
		version:      cmp.Or(options.Version, defaultServerVersion),
		schemaCache:  mcp.NewSchemaCache(),
		allowedTools: maps.Clone(options.AllowedTools),
		automation:   options.Automation,
		confirm:      options.ConfirmationIssuer,
		policy:       options.PolicyRevision,
		scope:        options.Scope,
		decoder:      options.DecoderAvailable,
	}, nil
}

func (s *Server) toolAllowed(name string) bool {
	return s.allowedTools == nil || s.allowedTools[name]
}

func (s *Server) controlToolsReady() bool {
	return s.automation != nil
}

func (s *Server) confirmationReady() bool {
	return s.confirm != nil && s.policy != ""
}

const defaultConfirmationTTL = 2 * time.Minute

// RunStdio serves MCP over process stdin/stdout until the peer disconnects or
// ctx is canceled. Callers must keep stdout exclusively reserved for MCP.
func (s *Server) RunStdio(ctx context.Context) error {
	return s.newProtocolServer().Run(ctx, &mcp.StdioTransport{})
}

func (s *Server) newProtocolServer() *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: s.name, Version: s.version},
		&mcp.ServerOptions{
			Capabilities: &mcp.ServerCapabilities{
				Tools:     &mcp.ToolCapabilities{},
				Resources: &mcp.ResourceCapabilities{},
			},
			SchemaCache: s.schemaCache,
		},
	)
	server.AddReceivingMiddleware(privateResourceCacheScope)
	s.registerTools(server)
	s.registerResources(server)
	return server
}

// privateResourceCacheScope preserves the caller-specific cache boundary after
// the SDK applies its public default to resource reads.
func privateResourceCacheScope(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		result, err := next(ctx, method, request)
		if resource, ok := result.(*mcp.ReadResourceResult); ok {
			resource.CacheScope = "private"
		}
		return result, err
	}
}
