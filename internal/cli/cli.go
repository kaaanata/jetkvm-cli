// Package cli implements the user- and agent-facing JetKVM command tree.
//
// Cobra owns command parsing and help generation. Device and MCP behavior is
// injected so this package remains an adapter over the shared application core.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
	"uuid"

	"github.com/kaaanata/jetkvm-cli/internal/automation"
	"github.com/kaaanata/jetkvm-cli/internal/buildinfo"
	"github.com/kaaanata/jetkvm-cli/internal/config"
	"github.com/kaaanata/jetkvm-cli/internal/confirmation"
	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/operation"
	"github.com/kaaanata/jetkvm-cli/internal/progress"
	setupcore "github.com/kaaanata/jetkvm-cli/internal/setup"
	"github.com/kaaanata/jetkvm-cli/internal/terminal"
	updatecore "github.com/kaaanata/jetkvm-cli/internal/update"
	"github.com/spf13/cobra"
)

// Exit codes are part of the CLI's stable machine-facing contract.
const (
	ExitOK          = 0
	ExitInternal    = 1
	ExitUsage       = 2
	ExitNotFound    = 3
	ExitAuth        = 4
	ExitUnavailable = 5
	ExitUnsupported = 6
	ExitConflict    = 7
	ExitAmbiguous   = 8
)

const outputAuto = ""

// MCPServeOptions is the CLI-to-MCP adapter boundary. The MCP implementation
// owns protocol details; the CLI owns user-facing argument validation.
type MCPServeOptions struct {
	Transport string
	Listen    string
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
}

// MCPServer runs the MCP adapter over the shared application core.
type MCPServer interface {
	Serve(context.Context, MCPServeOptions) error
}

// AutomationService is the CLI-facing control boundary implemented by the
// shared automation core. CLI commands never access JetKVM transports or RPCs.
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

// ReleaseInputRequest identifies an explicit neutralization operation. It is
// separate from an action batch because an empty or synthetic batch must never
// masquerade as terminal input release.
// InputReleaser is the dedicated core capability required by `input release`.
// A runtime that does not implement it fails closed instead of using close or
// direct HID as a substitute.
type InputReleaser interface {
	ReleaseInput(context.Context, automation.ReleaseInputRequest) (operation.Receipt, error)
}

// ConfirmationRequest binds an action-time confirmation to the exact device,
// control generation, operation, and normalized human-readable summary.
type ConfirmationRequest struct {
	DeviceID    domain.DeviceID
	Ref         control.Ref
	OperationID uuid.UUID
	Action      string
	Summary     string
	Interactive bool
	Binding     confirmation.Binding
}

// ConfirmationIssuer prompts only when Interactive is true, or redeems an
// already-issued internal proof for non-interactive execution. It returns a
// context carrying that proof for the shared core to verify at execution time.
type ConfirmationIssuer interface {
	Issue(context.Context, ConfirmationRequest) (context.Context, error)
}

type UpdateService interface {
	Resolve(context.Context, updatecore.Request) (updatecore.Resolution, error)
	Check(context.Context, updatecore.Resolution) (updatecore.CheckResult, error)
	Plan(updatecore.CheckResult) (updatecore.Plan, error)
	Apply(context.Context, updatecore.Plan) (updatecore.Result, error)
	Rollback(context.Context) (updatecore.Result, error)
}

type SetupService interface {
	Plan(context.Context, setupcore.PlanRequest) (setupcore.Plan, error)
	Apply(context.Context, setupcore.Plan) (setupcore.Receipt, error)
	Uninstall(context.Context, setupcore.Target, bool) (setupcore.Receipt, error)
	Doctor(context.Context, setupcore.Target, string) (setupcore.DoctorReport, error)
}

// Runtime contains the initialized application services for one CLI process.
// Close flushes durable state and releases process-owned resources.
type Runtime struct {
	Devices       domain.DeviceService
	Automation    AutomationService
	Releaser      InputReleaser
	Confirmations ConfirmationIssuer
	MCP           MCPServer
	OutputMode    string
	Close         func() error
}

// RuntimeLoader creates the shared core after Cobra has parsed the authoritative
// --config flag. CLI and MCP commands then receive the same service instances.
type RuntimeLoader interface {
	Load(context.Context, string) (Runtime, error)
}

// RuntimeLoaderFunc adapts a function to RuntimeLoader.
type RuntimeLoaderFunc func(context.Context, string) (Runtime, error)

func (f RuntimeLoaderFunc) Load(ctx context.Context, path string) (Runtime, error) {
	return f(ctx, path)
}

// Logger deliberately matches the structured shape used by charmbracelet/log
// without forcing callers or this package to a concrete logging implementation.
// Logs must be directed to stderr by the composition root.
type Logger interface {
	Debug(any, ...any)
	Info(any, ...any)
	Warn(any, ...any)
	Error(any, ...any)
}

// Dependencies are supplied by the executable's composition root.
type Dependencies struct {
	Devices       domain.DeviceService
	Automation    AutomationService
	Releaser      InputReleaser
	Confirmations ConfirmationIssuer
	MCP           MCPServer
	Version       buildinfo.Info
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
	IsTerminal    func(io.Writer) bool
	Logger        Logger
	Loader        RuntimeLoader
	ConfigPath    string
	Updater       UpdateService
	Setup         SetupService
	MCPLoader     func(context.Context, string) (MCPServer, func() error, error)
	OpenBrowser   func(context.Context, string) error
	InputTerminal func(io.Reader) bool
}

// App owns one configured command tree and its output policy.
type App struct {
	deps             Dependencies
	root             *cobra.Command
	outputMode       string
	configPath       string
	runtimeClose     func() error
	executionStarted bool
	presentationErr  error
	activity         *terminal.Activity
	executing        bool
	pending          []pendingResult
	verbose          bool
	failureStage     string
	executionContext context.Context
}

// New constructs the complete public command tree.
func New(deps Dependencies) *App {
	deps = withDefaults(deps)
	a := &App{deps: deps, outputMode: outputAuto, configPath: deps.ConfigPath}
	a.root = a.newRootCommand()
	return a
}

// Command exposes the Cobra tree for executable-level integration and help.
// Prefer Execute when the caller also needs stable exit-code handling.
func (a *App) Command() *cobra.Command { return a.root }

// Execute runs the command and renders failures to stderr using the selected
// output mode. Successful command results are the only non-MCP bytes written
// to stdout.
func (a *App) Execute(ctx context.Context, args []string) int {
	a.executing = true
	a.executionContext = ctx
	a.pending = nil
	a.failureStage = ""
	defer func() { a.executing = false; a.pending = nil; a.executionContext = nil }()
	a.executionStarted = false
	a.presentationErr = nil
	a.root.SetArgs(args)
	err := a.root.ExecuteContext(ctx)
	if a.presentationErr != nil {
		err = errors.Join(err, a.presentationErr)
	}
	if a.runtimeClose != nil {
		closeErr := a.runtimeClose()
		a.runtimeClose = nil
		err = errors.Join(err, closeErr)
	}
	stage := ""
	if a.activity != nil {
		stage = a.activity.Stage()
		err = errors.Join(err, a.activity.Close())
		if logger, ok := a.deps.Logger.(interface{ SetOutput(io.Writer) }); ok {
			logger.SetOutput(a.deps.Stderr)
		}
		a.activity = nil
	}
	if a.failureStage != "" {
		stage = a.failureStage
	}
	for _, result := range a.pending {
		err = errors.Join(err, a.emitResult(result.command, result.data, err != nil))
	}
	if err == nil {
		return ExitOK
	}

	mode, modeErr := a.resolvedOutputMode()
	if modeErr != nil {
		mode = a.defaultOutputMode()
		err = modeErr
	}
	if !a.executionStarted {
		if _, alreadyUsage := errors.AsType[*usageFailure](err); !alreadyUsage {
			err = usageError(err)
		}
	}
	if renderErr := renderFailureAt(a.deps.Stderr, mode, err, a.deps.IsTerminal(a.deps.Stderr), stage); renderErr != nil && a.deps.Logger != nil {
		a.deps.Logger.Error("render CLI failure", "error", renderErr)
	}
	return ExitCode(err)
}

func withDefaults(deps Dependencies) Dependencies {
	if deps.InputTerminal == nil {
		deps.InputTerminal = func(r io.Reader) bool { return terminal.IsTerminal(r) }
	}
	if deps.Stdin == nil {
		deps.Stdin = os.Stdin
	}
	if deps.Stdout == nil {
		deps.Stdout = os.Stdout
	}
	if deps.Stderr == nil {
		deps.Stderr = os.Stderr
	}
	if deps.IsTerminal == nil {
		deps.IsTerminal = isTerminalWriter
	}
	if deps.Version.Version == "" {
		deps.Version = buildinfo.Current()
	}
	return deps
}

func (a *App) newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:         "jetkvm",
		Short:       "Control and inspect JetKVM devices",
		Annotations: map[string]string{"runtime": "skip"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usageError(fmt.Errorf("unknown command %q", args[0]))
			}
			return cmd.Help()
		},
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			a.executionStarted = true
			if a.executing {
				cmd.SetContext(a.executionContext)
			}
			// Attach after runtime output defaults are known. Maintenance commands
			// have no runtime and can attach immediately.
			if cmd.Name() == "serve" && cmd.Parent() != nil && cmd.Parent().Name() == "mcp" && a.deps.MCPLoader != nil {
				server, close, err := a.deps.MCPLoader(cmd.Context(), a.configPath)
				if err != nil {
					return err
				}
				a.deps.MCP, a.runtimeClose = server, close
				return nil
			}
			if cmd.Annotations["runtime"] == "skip" || a.deps.Loader == nil {
				return a.startActivity(cmd)
			}
			if strings.TrimSpace(a.configPath) == "" {
				return usageError(errors.New("--config is required"))
			}
			if cmd.Name() == "list" && cmd.Parent() != nil && cmd.Parent().Name() == "devices" && a.canGuideDevice() {
				needed, err := a.deviceSetupNeeded()
				if err != nil {
					return err
				}
				if needed {
					if _, err := a.guideDevice(cmd.Context()); err != nil {
						return err
					}
				}
			}
			runtime, err := a.deps.Loader.Load(cmd.Context(), a.configPath)
			if errors.Is(err, config.ErrMissing) && a.canGuideDevice() && (cmd.Parent() == nil || cmd.Parent().Name() != "mcp") {
				if _, setupErr := a.guideDevice(cmd.Context()); setupErr != nil {
					return setupErr
				}
				runtime, err = a.deps.Loader.Load(cmd.Context(), a.configPath)
			}
			if err != nil {
				return err
			}
			if runtime.Devices == nil || runtime.MCP == nil || runtime.Close == nil {
				if runtime.Close != nil {
					_ = runtime.Close()
				}
				return errors.New("runtime loader returned incomplete services")
			}
			a.deps.Devices = runtime.Devices
			a.deps.Automation = runtime.Automation
			a.deps.Releaser = runtime.Releaser
			a.deps.Confirmations = runtime.Confirmations
			a.deps.MCP = runtime.MCP
			a.runtimeClose = runtime.Close
			if a.outputMode == outputAuto && runtime.OutputMode != "" && runtime.OutputMode != "auto" {
				a.outputMode = runtime.OutputMode
			}
			return a.startActivity(cmd)
		},
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          noArgs,
	}
	root.SetIn(a.deps.Stdin)
	root.SetOut(a.deps.Stdout)
	root.SetErr(a.deps.Stderr)
	root.SetHelpFunc(func(cmd *cobra.Command, _ []string) { a.presentationErr = a.writeHelp(cmd, cmd.OutOrStdout()) })
	root.SetUsageFunc(func(cmd *cobra.Command) error { return a.writeHelp(cmd, cmd.ErrOrStderr()) })
	root.PersistentFlags().StringVarP(&a.outputMode, "output", "o", outputAuto, "output format: json or text (defaults to text on a TTY and JSON otherwise)")
	root.PersistentFlags().StringVar(&a.configPath, "config", a.configPath, "path to the strict JetKVM JSON configuration")
	root.PersistentFlags().BoolVarP(&a.verbose, "verbose", "v", false, "include diagnostic identifiers and receipt details in human output")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageError(err)
	})

	root.AddCommand(
		a.newVersionCommand(),
		a.newDevicesCommand(),
		a.newStatusCommand(),
		a.newCapabilitiesCommand(),
		a.newDoctorCommand(),
		a.newSetupCommand(),
		a.newConfigCommand(),
		a.newUpdateCommand(),
		a.newInputCommand(),
		a.newScreenshotCommand(),
		a.newPowerCommand(),
		a.newMCPCommand(),
	)
	return root
}

func (a *App) newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:         "version",
		Short:       "Show build version information",
		Args:        noArgs,
		Annotations: map[string]string{"runtime": "skip"},
		RunE: func(*cobra.Command, []string) error {
			return a.writeResult("version", a.deps.Version)
		},
	}
}

func (a *App) newDevicesCommand() *cobra.Command {
	devices := &cobra.Command{Use: "devices", Short: "Manage configured JetKVM devices", Args: noArgs}
	devices.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List explicitly configured devices",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.deps.Devices == nil {
				return unavailableDependency("device service")
			}
			items, err := a.deps.Devices.ListDevices(cmd.Context())
			if err != nil {
				return err
			}
			result := deviceListResult{Devices: items}
			return a.writeResult("devices.list", result)
		},
	})
	return devices
}

func (a *App) newStatusCommand() *cobra.Command {
	detail := string(domain.StatusBasic)
	cmd := &cobra.Command{
		Use:   "status <device>",
		Short: "Read source-attributed device status",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			parsedDetail, err := parseStatusDetail(detail)
			if err != nil {
				return err
			}
			id, err := a.resolveDevice(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			progress.Stage(cmd.Context(), "Reading device status")
			status, err := a.deps.Devices.GetStatus(cmd.Context(), id, parsedDetail)
			if err != nil {
				return err
			}
			return a.writeResult("status", status)
		},
	}
	cmd.Flags().StringVar(&detail, "detail", detail, "status detail: basic, standard, or diagnostic")
	return cmd
}

func (a *App) newCapabilitiesCommand() *cobra.Command {
	var refresh bool
	cmd := &cobra.Command{
		Use:   "capabilities <device>",
		Short: "Show compiled, configured, firmware, and runtime capabilities",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := a.resolveDevice(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			progress.Stage(cmd.Context(), "Checking device capabilities")
			snapshot, err := a.deps.Devices.GetCapabilities(cmd.Context(), id, refresh)
			if err != nil {
				return err
			}
			return a.writeResult("capabilities", snapshot)
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false, "refresh device capability evidence")
	return cmd
}

func (a *App) newDoctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor <device>",
		Short: "Collect a source-attributed health and capability report",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := a.resolveDevice(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			progress.Stage(cmd.Context(), "Checking device health")
			status, err := a.deps.Devices.GetStatus(cmd.Context(), id, domain.StatusBasic)
			if err != nil {
				return err
			}
			capabilities, err := a.deps.Devices.GetCapabilities(cmd.Context(), id, true)
			if err != nil {
				return err
			}
			report := newDoctorReport(status, capabilities)
			return a.writeResult("doctor", report)
		},
	}
}

func (a *App) newMCPCommand() *cobra.Command {
	mcp := &cobra.Command{Use: "mcp", Short: "Run the built-in MCP server", Args: noArgs}
	transport := "stdio"
	listen := "127.0.0.1:0"
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Serve MCP over stdio or loopback Streamable HTTP",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.deps.MCP == nil {
				return unavailableDependency("MCP server")
			}
			if transport != "stdio" && transport != "http" {
				return usageError(fmt.Errorf("unsupported MCP transport %q", transport))
			}
			if transport == "stdio" && cmd.Flags().Changed("listen") {
				return usageError(errors.New("--listen is only valid with --transport=http"))
			}
			if transport == "http" {
				if err := validateLoopbackListen(listen); err != nil {
					return err
				}
			}
			return a.deps.MCP.Serve(cmd.Context(), MCPServeOptions{
				Transport: transport,
				Listen:    listen,
				Stdin:     a.deps.Stdin,
				Stdout:    a.deps.Stdout,
				Stderr:    a.deps.Stderr,
			})
		},
	}
	serve.Flags().StringVar(&transport, "transport", transport, "MCP transport: stdio or http")
	serve.Flags().StringVar(&listen, "listen", listen, "loopback listen address for HTTP transport")
	mcp.AddCommand(serve)
	return mcp
}

func (a *App) resolveDevice(ctx context.Context, selector string) (domain.DeviceID, error) {
	if a.deps.Devices == nil {
		return "", unavailableDependency("device service")
	}
	devices, err := a.deps.Devices.ListDevices(ctx)
	if err != nil {
		return "", err
	}
	var match domain.DeviceID
	for _, device := range devices {
		if string(device.ID) == selector {
			return device.ID, nil
		}
		if device.Alias != selector {
			continue
		}
		if match != "" && match != device.ID {
			return "", usageError(fmt.Errorf("device alias %q is ambiguous; use a device ID", selector))
		}
		match = device.ID
	}
	if match != "" {
		return match, nil
	}
	return "", fmt.Errorf("%w: %s", domain.ErrDeviceNotExposed, selector)
}

func parseStatusDetail(value string) (domain.StatusDetail, error) {
	detail := domain.StatusDetail(value)
	switch detail {
	case domain.StatusBasic, domain.StatusStandard, domain.StatusDiagnostic:
		return detail, nil
	default:
		return "", usageError(fmt.Errorf("invalid status detail %q", value))
	}
}

func validateLoopbackListen(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return usageError(fmt.Errorf("invalid HTTP listen address %q: %w", address, err))
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return usageError(fmt.Errorf("HTTP listen address must use a numeric loopback IP, got %q", host))
	}
	return nil
}

func noArgs(_ *cobra.Command, args []string) error {
	if len(args) != 0 {
		return usageError(fmt.Errorf("accepts no arguments, received %d", len(args)))
	}
	return nil
}

func exactArgs(count int) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != count {
			return usageError(fmt.Errorf("requires exactly %d argument(s), received %d", count, len(args)))
		}
		if strings.TrimSpace(args[0]) == "" {
			return usageError(errors.New("device selector must not be empty"))
		}
		return nil
	}
}

func isTerminalWriter(w io.Writer) bool {
	return terminal.IsTerminal(w)
}

type deviceListResult struct {
	Devices []domain.Device `json:"devices"`
}

type doctorReport struct {
	Healthy      bool                      `json:"healthy"`
	ObservedAt   time.Time                 `json:"observed_at"`
	Status       domain.DeviceStatus       `json:"status"`
	Capabilities domain.CapabilitySnapshot `json:"capabilities"`
	Warnings     []string                  `json:"warnings,omitempty"`
}

func newDoctorReport(status domain.DeviceStatus, capabilities domain.CapabilitySnapshot) doctorReport {
	warnings := make([]string, 0)
	if !status.Reachable {
		warnings = append(warnings, "device is not reachable")
	}
	for _, capability := range capabilities.Items {
		if capability.Configured && !capability.CurrentlyReady {
			warnings = append(warnings, fmt.Sprintf("capability %s is configured but not ready", capability.Name))
		}
	}
	observedAt := status.Observed
	if capabilities.Observed.After(observedAt) {
		observedAt = capabilities.Observed
	}
	return doctorReport{
		Healthy:      status.Reachable && len(warnings) == 0,
		ObservedAt:   observedAt,
		Status:       status,
		Capabilities: capabilities,
		Warnings:     warnings,
	}
}
