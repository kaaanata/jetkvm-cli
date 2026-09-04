// Package app assembles the shared JetKVM domain core used by CLI and MCP.
package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/kaaanata/jetkvm-cli/internal/automation"
	"github.com/kaaanata/jetkvm-cli/internal/config"
	"github.com/kaaanata/jetkvm-cli/internal/confirmation"
	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/credentials"
	"github.com/kaaanata/jetkvm-cli/internal/device"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/inventory"
	"github.com/kaaanata/jetkvm-cli/internal/jetkvm"
	"github.com/kaaanata/jetkvm-cli/internal/mcpserver"
	"github.com/kaaanata/jetkvm-cli/internal/operation"
	"github.com/kaaanata/jetkvm-cli/internal/policy"
	"github.com/kaaanata/jetkvm-cli/internal/store"
	"github.com/kaaanata/jetkvm-cli/internal/video"
)

// Runtime is the one composition root shared by CLI commands and MCP tools.
type Runtime struct {
	Config       config.Config
	Devices      domain.DeviceService
	MCP          *mcpserver.Server
	Policy       *policy.Compiled
	Store        *store.Store
	Automation   *AutomationService
	Confirmation *confirmation.Authority

	closeOnce sync.Once
	closeErr  error
}

const runtimeDrainTimeout = 10 * time.Second

// Load validates the complete configuration and constructs all authoritative
// services before publishing the runtime to any command or MCP handler.
func Load(ctx context.Context, path, version string) (_ *Runtime, err error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	if err := ensureStatePath(cfg.State.Path); err != nil {
		return nil, err
	}
	secret, err := loadOrCreateRuntimeSecret(ctx, filepath.Dir(cfg.State.Path))
	if err != nil {
		return nil, err
	}

	database, err := store.Open(ctx, cfg.State.Path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, database.Close())
		}
	}()

	catalog := inventory.Static()
	compiledPolicy, err := policy.Compile(cfg, catalog)
	if err != nil {
		return nil, fmt.Errorf("compile policy: %w", err)
	}

	targets := make([]device.Target, 0, len(cfg.Devices))
	clients := make(map[domain.DeviceID]*jetkvm.Client, len(cfg.Devices))
	for alias, targetConfig := range cfg.Devices {
		if !targetConfig.Exposed {
			continue
		}
		provider, err := credentials.New(targetConfig.Credentials)
		if err != nil {
			return nil, fmt.Errorf("configure credentials for device %q: %w", alias, err)
		}
		httpClient, err := deviceHTTPClient(targetConfig)
		if err != nil {
			return nil, fmt.Errorf("configure TLS for device %q: %w", alias, err)
		}
		client, err := jetkvm.NewClient(jetkvm.Config{
			Origin:         targetConfig.Origin,
			AllowPlainHTTP: targetConfig.AllowPlainHTTP,
			Credentials:    provider,
			HTTPClient:     httpClient,
		})
		if err != nil {
			return nil, fmt.Errorf("configure device %q: %w", alias, err)
		}
		canonicalOrigin := client.Origin()
		deviceID := domain.DeviceID(targetConfig.DeviceID)
		clients[deviceID] = client
		if _, _, err := database.PinIdentity(ctx, store.IdentityPin{
			Alias:    alias,
			Origin:   canonicalOrigin,
			DeviceID: deviceID,
		}, time.Now()); err != nil {
			return nil, fmt.Errorf("pin configured identity for device %q: %w", alias, err)
		}
		targets = append(targets, device.Target{
			Device: domain.Device{
				ID:              deviceID,
				Alias:           alias,
				Origin:          canonicalOrigin,
				Exposed:         true,
				Permissions:     slices.Clone(targetConfig.Permissions),
				TakeoverAllowed: targetConfig.Takeover.Allowed,
				Labels:          targetConfig.Labels,
			},
			Client: client,
		})
	}

	deviceService, err := device.NewService(device.ServiceConfig{Targets: targets})
	if err != nil {
		return nil, err
	}
	authorized := &authorizedDevices{next: deviceService, policy: compiledPolicy}
	digester, err := operation.NewDigester(secret[:])
	if err != nil {
		return nil, fmt.Errorf("configure operation digester: %w", err)
	}
	confirmationAuthority, err := confirmation.NewAuthority(confirmation.Config{
		Key:    secret[:],
		Nonces: confirmation.NewMemoryNonceStore(),
	})
	if err != nil {
		return nil, fmt.Errorf("configure confirmation authority: %w", err)
	}
	mcpConfirmation, err := mcpserver.NewConfirmationIssuer(secret[:], confirmation.DefaultTTL, confirmationAuthority)
	if err != nil {
		return nil, fmt.Errorf("configure MCP confirmation continuation: %w", err)
	}
	registry, err := newControlRegistry(clients, cfg, filepath.Join(filepath.Dir(cfg.State.Path), "locks"))
	if err != nil {
		return nil, err
	}
	automationService, err := automation.NewService(automation.Config{
		Registry:      registry,
		Policy:        compiledPolicy,
		Operations:    operation.NewService(database),
		Digester:      digester,
		Confirmations: confirmationAuthority,
	})
	if err != nil {
		drainCtx, cancel := context.WithTimeoutCause(context.Background(), runtimeDrainTimeout, errors.New("runtime construction cleanup timed out"))
		defer cancel()
		return nil, errors.Join(err, registry.Drain(drainCtx))
	}
	defer func() {
		if err != nil {
			drainCtx, cancel := context.WithTimeoutCause(context.Background(), runtimeDrainTimeout, errors.New("runtime construction cleanup timed out"))
			err = errors.Join(err, automationService.Drain(drainCtx))
			cancel()
		}
	}()
	allowedTools := make(map[string]bool)
	for _, tool := range compiledPolicy.Tools(policy.Scope{}, "") {
		allowedTools[tool.Name] = true
	}
	applicationAutomation := newAutomationService(automationService, cfg)
	mcp, err := mcpserver.New(authorized, mcpserver.Options{
		Version:            version,
		DecoderAvailable:   video.EmbeddedDecoder().Available(),
		AllowedTools:       allowedTools,
		Automation:         applicationAutomation,
		ConfirmationIssuer: mcpConfirmation,
		PolicyRevision:     compiledPolicy.Revision(),
	})
	if err != nil {
		return nil, err
	}
	return &Runtime{
		Config:       cfg,
		Devices:      authorized,
		MCP:          mcp,
		Policy:       compiledPolicy,
		Store:        database,
		Automation:   applicationAutomation,
		Confirmation: confirmationAuthority,
	}, nil
}

// Close drains all device actors before releasing the durable store.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.closeErr = closeRuntime(r.Automation, r.Store)
	})
	return r.closeErr
}

type automationDrainer interface {
	Drain(context.Context) error
}

type storeCloser interface {
	Close() error
}

func closeRuntime(automationService automationDrainer, database storeCloser) error {
	var drainErr error
	if automationService != nil {
		drainCtx, cancel := context.WithTimeoutCause(context.Background(), runtimeDrainTimeout, errors.New("automation drain timed out"))
		drainErr = automationService.Drain(drainCtx)
		cancel()
	}
	var closeErr error
	if database != nil {
		closeErr = database.Close()
	}
	return errors.Join(drainErr, closeErr)
}

func newControlRegistry(clients map[domain.DeviceID]*jetkvm.Client, cfg config.Config, lockDirectory string) (*control.Registry, error) {
	var factory control.SessionFactory
	if len(clients) == 0 {
		factory = unavailableSessionFactory{}
	} else {
		configured, err := automation.NewSessionFactory(clients, jetkvm.SessionConfig{})
		if err != nil {
			return nil, fmt.Errorf("configure JetKVM session factory: %w", err)
		}
		factory = configured
	}
	locker, err := automation.NewFileLocker(lockDirectory)
	if err != nil {
		return nil, fmt.Errorf("configure device locks: %w", err)
	}
	idleTimeout, absoluteLifetime := strictestSessionLimits(cfg)
	registry, err := control.NewRegistry(control.Config{
		Factory:          factory,
		Locker:           locker,
		IdleTimeout:      idleTimeout,
		AbsoluteLifetime: absoluteLifetime,
	})
	if err != nil {
		return nil, fmt.Errorf("configure control registry: %w", err)
	}
	return registry, nil
}

type unavailableSessionFactory struct{}

func (unavailableSessionFactory) Open(context.Context, domain.DeviceID, uint64, []string) (control.Session, error) {
	return nil, domain.ErrDeviceNotExposed
}

// strictestSessionLimits selects conservative process-wide registry defaults.
// AutomationService supplies the authoritative per-device values on Open, so
// devices still share exactly one registry and one actor per stable identity.
func strictestSessionLimits(cfg config.Config) (time.Duration, time.Duration) {
	idleTimeout := control.DefaultIdleTimeout
	absoluteLifetime := control.DefaultAbsoluteLifetime
	initialized := false
	for _, device := range cfg.Devices {
		if !device.Exposed {
			continue
		}
		if !initialized {
			idleTimeout = device.Session.IdleTimeout.Duration
			absoluteLifetime = device.Session.AbsoluteLifetime.Duration
			initialized = true
			continue
		}
		idleTimeout = min(idleTimeout, device.Session.IdleTimeout.Duration)
		absoluteLifetime = min(absoluteLifetime, device.Session.AbsoluteLifetime.Duration)
	}
	return min(idleTimeout, absoluteLifetime), absoluteLifetime
}

func ensureStatePath(path string) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("create state database: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close state database bootstrap file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("protect state database: %w", err)
	}
	return nil
}

func deviceHTTPClient(cfg config.DeviceConfig) (*http.Client, error) {
	if cfg.TLS.Mode != config.TLSPinned {
		return nil, nil
	}
	return jetkvm.NewPinnedHTTPClient(cfg.TLS.SPKISHA256)
}
