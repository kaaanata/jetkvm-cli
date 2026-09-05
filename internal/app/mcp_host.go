package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync"

	"github.com/kaaanata/jetkvm-cli/internal/config"
	"github.com/kaaanata/jetkvm-cli/internal/device"
	"github.com/kaaanata/jetkvm-cli/internal/inventory"
	"github.com/kaaanata/jetkvm-cli/internal/mcpserver"
	"github.com/kaaanata/jetkvm-cli/internal/onboarding"
	"github.com/kaaanata/jetkvm-cli/internal/policy"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPHost owns the bootstrap-to-ready transition and runtime lifetime. Its gate
// joins in-flight calls before replacing protocol handlers or closing a runtime.
type MCPHost struct {
	gate          sync.RWMutex
	runtime       *Runtime
	protocol      *mcp.Server
	browser       *onboarding.Browser
	path, version string
	closed        bool
	closeOnce     sync.Once
	closeErr      error
}

func NewMCPHost(ctx context.Context, path, version string) (*MCPHost, error) {
	h := &MCPHost{path: path, version: version}
	runtime, err := Load(ctx, path, version)
	if err != nil && !errors.Is(err, config.ErrMissing) {
		return nil, err
	}
	h.runtime = runtime
	service, err := onboarding.New(onboarding.Options{Path: path, Change: h.change})
	if err != nil {
		_ = runtime.Close()
		return nil, err
	}
	browser, err := onboarding.NewBrowser(service)
	if err != nil {
		_ = runtime.Close()
		return nil, err
	}
	h.browser = browser
	var adapter *mcpserver.Server
	if runtime != nil {
		adapter = runtime.MCP
	} else {
		empty, err := device.NewService(device.ServiceConfig{})
		if err != nil {
			_ = browser.Close()
			return nil, err
		}
		adapter, err = mcpserver.New(empty, mcpserver.Options{Version: version, AllowedTools: map[string]bool{"jetkvm_list_devices": true, "jetkvm_setup": true, "jetkvm_setup_status": true}})
		if err != nil {
			_ = browser.Close()
			return nil, err
		}
	}
	h.protocol = adapter.ProtocolServer()
	adapter.Install(h.protocol, browser)
	h.protocol.AddReceivingMiddleware(h.dispatch)
	return h, nil
}

func (h *MCPHost) dispatch(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		for {
			h.gate.RLock()
			if h.closed {
				h.gate.RUnlock()
				return nil, errors.New("JetKVM is shutting down")
			}
			// A pending configuration must not prevent a stateless HTTP peer
			// from initializing before it submits terminal cleanup, or prevent
			// cancellation of an in-flight operation.
			switch method {
			case "initialize", "notifications/initialized", "notifications/cancelled", "ping":
				defer h.gate.RUnlock()
				return next(ctx, method, request)
			}
			revision, err := h.diskRevision()
			if err == nil && revision == h.runtimeRevision() {
				defer h.gate.RUnlock()
				return next(ctx, method, request)
			}
			h.gate.RUnlock()
			h.gate.Lock()
			err = h.reconcile(ctx)
			h.gate.Unlock()
			if err != nil {
				// Configuration changes fence new effects, never the terminal
				// cleanup and receipt reads needed to release existing controls.
				if cleanupRequest(request) {
					h.gate.RLock()
					defer h.gate.RUnlock()
					if h.closed {
						return nil, context.Canceled
					}
					return next(ctx, method, request)
				}
				return nil, errors.New(onboarding.PublicMessage(err))
			}
		}
	}
}

func cleanupRequest(request mcp.Request) bool {
	call, ok := request.(*mcp.CallToolRequest)
	if !ok || call.Params == nil {
		return false
	}
	switch call.Params.Name {
	case "jetkvm_close_control", "jetkvm_get_control", "jetkvm_get_operation", "jetkvm_setup_status":
		return true
	}
	return false
}

func (h *MCPHost) diskRevision() (string, error) {
	data, err := os.ReadFile(h.path)
	if errors.Is(err, os.ErrNotExist) && h.runtime == nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return config.Revision(data), nil
}

func (h *MCPHost) runtimeRevision() string {
	if h.runtime == nil {
		return ""
	}
	return h.runtime.ConfigRevision
}

// reconcile is called under the write gate. An external CLI update does not
// revoke cleanup access or disconnect active hardware sessions.
func (h *MCPHost) reconcile(ctx context.Context) error {
	if h.closed {
		return context.Canceled
	}
	revision, err := h.diskRevision()
	if err != nil {
		return err
	}
	if revision == h.runtimeRevision() {
		return nil
	}
	if h.runtime != nil {
		quiet, err := h.runtime.registry.Quiescent(ctx)
		if err != nil {
			return err
		}
		if !quiet {
			return onboarding.ErrActiveControls
		}
	}
	return h.activate(ctx)
}

func (h *MCPHost) activate(ctx context.Context) error {
	next, err := Load(ctx, h.path, h.version)
	if err != nil {
		return errors.Join(onboarding.ErrActivation, err)
	}
	previous := h.runtime
	h.runtime = next
	for _, tool := range inventory.Static().All() {
		h.protocol.RemoveTools(tool.Name)
	}
	mcpserver.ClearResources(h.protocol)
	next.MCP.Install(h.protocol, h.browser)
	if err := previous.Close(); err != nil {
		return errors.Join(onboarding.ErrActivation, err)
	}
	return nil
}

func (h *MCPHost) change(ctx context.Context, tool string, commit func() (onboarding.Receipt, error)) (onboarding.Receipt, error) {
	h.gate.Lock()
	defer h.gate.Unlock()
	if h.closed {
		return onboarding.Receipt{}, context.Canceled
	}
	if h.runtime != nil {
		if !h.runtime.Policy.Evaluate(policy.Evaluation{ToolName: tool}).Allowed {
			return onboarding.Receipt{}, onboarding.ErrPolicyDenied
		}
		quiet, err := h.runtime.registry.Quiescent(ctx)
		if err != nil {
			return onboarding.Receipt{}, err
		}
		if !quiet {
			return onboarding.Receipt{}, onboarding.ErrActiveControls
		}
	}
	receipt, err := commit()
	if err != nil {
		return receipt, err
	}
	// Use a fresh lifetime after commit: cancellation must not turn a durable
	// enrollment into a silently abandoned activation.
	return receipt, h.activate(context.WithoutCancel(ctx))
}

func (h *MCPHost) RunStdio(ctx context.Context) error {
	return h.protocol.Run(ctx, &mcp.StdioTransport{})
}
func (h *MCPHost) NewStatelessHTTPServer(cfg mcpserver.HTTPConfig) (*http.Server, error) {
	return mcpserver.NewHTTPServer(h.protocol, cfg)
}

func (h *MCPHost) Close() error {
	h.closeOnce.Do(func() {
		// Browser handlers may need the write gate; cancel and join them before
		// acquiring it for terminal runtime shutdown.
		browserErr := h.browser.Close()
		h.gate.Lock()
		defer h.gate.Unlock()
		h.closed = true
		h.closeErr = errors.Join(browserErr, h.runtime.Close())
	})
	return h.closeErr
}
