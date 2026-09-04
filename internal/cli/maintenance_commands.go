package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	setupcore "github.com/kaaanata/jetkvm-cli/internal/setup"
	"github.com/kaaanata/jetkvm-cli/internal/terminal"
	updatecore "github.com/kaaanata/jetkvm-cli/internal/update"
	"github.com/spf13/cobra"
)

func (a *App) newUpdateCommand() *cobra.Command {
	var checkOnly, dryRun, yes, allowDowngrade bool
	var version, channel string
	command := &cobra.Command{
		Use:         "update",
		Short:       "Check for and install JetKVM CLI updates",
		Args:        noArgs,
		Annotations: map[string]string{"runtime": "skip"},
		RunE: func(command *cobra.Command, _ []string) error {
			if a.deps.Updater == nil {
				return unavailableDependency("update service")
			}
			if checkOnly && dryRun {
				return usageError(errors.New("--check and --dry-run cannot be combined"))
			}
			request := updatecore.Request{Version: version, Channel: updatecore.Channel(channel), AllowDowngrade: allowDowngrade}
			resolution, err := a.deps.Updater.Resolve(command.Context(), request)
			if err != nil {
				return err
			}
			checked, err := a.deps.Updater.Check(command.Context(), resolution)
			if err != nil {
				return err
			}
			if checkOnly {
				return a.writeResult("update.check", checked)
			}
			plan, err := a.deps.Updater.Plan(checked)
			if err != nil {
				return err
			}
			if dryRun {
				return a.writeResult("update.plan", plan)
			}
			if plan.Action == updatecore.ActionSelfReplace {
				if err := a.confirmMaintenance("Replace the current JetKVM CLI with "+plan.TargetVersion+"?", yes); err != nil {
					return err
				}
			}
			result, err := a.deps.Updater.Apply(command.Context(), plan)
			if err != nil {
				return err
			}
			return a.writeResult("update", result)
		},
	}
	command.Flags().BoolVar(&checkOnly, "check", false, "check for an update without changing the installation")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "resolve and print the update plan without applying it")
	command.Flags().BoolVarP(&yes, "yes", "y", false, "confirm the self-update without prompting")
	command.Flags().StringVar(&version, "version", "", "install an exact semantic version")
	command.Flags().StringVar(&channel, "channel", string(updatecore.ChannelStable), "release channel: stable or prerelease")
	command.Flags().BoolVar(&allowDowngrade, "allow-downgrade", false, "allow an explicitly versioned downgrade")

	rollbackYes := false
	rollback := &cobra.Command{
		Use:         "rollback",
		Short:       "Restore the previous verified standalone installation",
		Args:        noArgs,
		Annotations: map[string]string{"runtime": "skip"},
		RunE: func(command *cobra.Command, _ []string) error {
			if a.deps.Updater == nil {
				return unavailableDependency("update service")
			}
			if err := a.confirmMaintenance("Restore the previous JetKVM CLI version?", rollbackYes); err != nil {
				return err
			}
			result, err := a.deps.Updater.Rollback(command.Context())
			if err != nil {
				return err
			}
			return a.writeResult("update.rollback", result)
		},
	}
	rollback.Flags().BoolVarP(&rollbackYes, "yes", "y", false, "confirm rollback without prompting")
	command.AddCommand(rollback)
	return command
}

type setupFlags struct {
	mode      string
	scope     string
	workspace string
	migrate   bool
	dryRun    bool
	yes       bool
}

func (a *App) newSetupCommand() *cobra.Command {
	flags := new(setupFlags)
	command := &cobra.Command{
		Use:         "setup",
		Short:       "Install JetKVM MCP and skills into coding agents",
		Args:        noArgs,
		Annotations: map[string]string{"runtime": "skip"},
		RunE: func(command *cobra.Command, _ []string) error {
			return a.runSetupMany(command.Context(), []setupcore.Host{setupcore.HostCodex, setupcore.HostClaudeCode}, *flags)
		},
	}
	addSetupFlags(command, flags)
	command.AddCommand(a.newSetupHostCommand(setupcore.HostCodex, flags), a.newSetupHostCommand(setupcore.HostClaudeCode, flags))
	command.AddCommand(a.newSetupDoctorCommand(flags), a.newSetupUninstallCommand(flags))
	return command
}

func addSetupFlags(command *cobra.Command, flags *setupFlags) {
	command.PersistentFlags().StringVar(&flags.mode, "mode", string(setupcore.ModePlugin), "integration mode: plugin or direct")
	command.PersistentFlags().StringVar(&flags.scope, "scope", string(setupcore.ScopeUser), "host scope: user, project, or local")
	command.PersistentFlags().StringVar(&flags.workspace, "workspace", "", "absolute workspace for project or local scope")
	command.PersistentFlags().BoolVar(&flags.migrate, "migrate", false, "migrate an equivalent legacy direct MCP integration")
	command.PersistentFlags().BoolVar(&flags.dryRun, "dry-run", false, "inspect and print the setup plan without changing the host")
	command.PersistentFlags().BoolVarP(&flags.yes, "yes", "y", false, "confirm setup or uninstall without prompting")
}

func (a *App) newSetupHostCommand(host setupcore.Host, flags *setupFlags) *cobra.Command {
	return &cobra.Command{
		Use:         string(host),
		Short:       "Install JetKVM into " + string(host),
		Args:        noArgs,
		Annotations: map[string]string{"runtime": "skip"},
		RunE: func(command *cobra.Command, _ []string) error {
			return a.runSetupMany(command.Context(), []setupcore.Host{host}, *flags)
		},
	}
}

func (a *App) runSetupMany(ctx context.Context, hosts []setupcore.Host, flags setupFlags) error {
	if a.deps.Setup == nil {
		return unavailableDependency("agent setup service")
	}
	var plans []setupcore.Plan
	var unavailable []error
	for _, host := range hosts {
		target, err := setupTarget(host, flags)
		if err != nil {
			return err
		}
		plan, err := a.deps.Setup.Plan(ctx, setupcore.PlanRequest{
			Target: target, PluginVersion: strings.TrimPrefix(a.deps.Version.Version, "v"), Migrate: flags.migrate, DryRun: flags.dryRun,
		})
		if errors.Is(err, setupcore.ErrHostUnavailable) && len(hosts) > 1 {
			unavailable = append(unavailable, err)
			continue
		}
		if err != nil {
			return err
		}
		plans = append(plans, plan)
	}
	if flags.dryRun {
		if len(plans) == 0 {
			return errors.Join(unavailable...)
		}
		return a.writeResult("setup.plan", plans)
	}
	if len(plans) == 0 {
		return errors.Join(unavailable...)
	}
	for _, plan := range plans {
		if len(plan.Steps) > 0 {
			if err := a.confirmMaintenance("Install the JetKVM plugin and MCP integration into "+string(plan.Target.Host)+"?", flags.yes); err != nil {
				return err
			}
		}
	}
	var receipts []setupcore.Receipt
	for _, plan := range plans {
		receipt, err := a.deps.Setup.Apply(ctx, plan)
		if err != nil {
			return err
		}
		receipts = append(receipts, receipt)
	}
	if len(receipts) == 0 {
		return errors.Join(unavailable...)
	}
	return a.writeResult("setup", receipts)
}

func (a *App) newSetupDoctorCommand(flags *setupFlags) *cobra.Command {
	return &cobra.Command{
		Use:         "doctor [codex|claude-code]",
		Short:       "Inspect agent host, marketplace, plugin, and MCP readiness",
		Args:        maximumArgs(1),
		Annotations: map[string]string{"runtime": "skip"},
		RunE: func(command *cobra.Command, args []string) error {
			if a.deps.Setup == nil {
				return unavailableDependency("agent setup service")
			}
			hosts, err := setupHosts(args)
			if err != nil {
				return err
			}
			var reports []setupcore.DoctorReport
			for _, host := range hosts {
				target, err := setupTarget(host, *flags)
				if err != nil {
					return err
				}
				report, err := a.deps.Setup.Doctor(command.Context(), target, strings.TrimPrefix(a.deps.Version.Version, "v"))
				if errors.Is(err, setupcore.ErrHostUnavailable) && len(hosts) > 1 {
					continue
				}
				if err != nil {
					return err
				}
				reports = append(reports, report)
			}
			return a.writeResult("setup.doctor", reports)
		},
	}
}

func (a *App) newSetupUninstallCommand(flags *setupFlags) *cobra.Command {
	return &cobra.Command{
		Use:         "uninstall <codex|claude-code>",
		Short:       "Remove only integration components owned by JetKVM setup",
		Args:        exactArgs(1),
		Annotations: map[string]string{"runtime": "skip"},
		RunE: func(command *cobra.Command, args []string) error {
			if a.deps.Setup == nil {
				return unavailableDependency("agent setup service")
			}
			host, err := parseSetupHost(args[0])
			if err != nil {
				return err
			}
			target, err := setupTarget(host, *flags)
			if err != nil {
				return err
			}
			if !flags.dryRun {
				if err := a.confirmMaintenance("Remove the JetKVM integration from "+string(host)+"?", flags.yes); err != nil {
					return err
				}
			}
			receipt, err := a.deps.Setup.Uninstall(command.Context(), target, flags.dryRun)
			if err != nil {
				return err
			}
			return a.writeResult("setup.uninstall", receipt)
		},
	}
}

func setupTarget(host setupcore.Host, flags setupFlags) (setupcore.Target, error) {
	target := setupcore.Target{Host: host, Mode: setupcore.Mode(flags.mode), Scope: setupcore.Scope(flags.scope)}
	if target.Scope != setupcore.ScopeUser {
		workspace := flags.workspace
		if workspace == "" {
			workspace = "."
		}
		absolute, err := filepath.Abs(workspace)
		if err != nil {
			return setupcore.Target{}, usageError(err)
		}
		target.Workspace = filepath.Clean(absolute)
	}
	if err := target.Validate(); err != nil {
		return setupcore.Target{}, usageError(err)
	}
	return target, nil
}

func setupHosts(args []string) ([]setupcore.Host, error) {
	if len(args) == 0 {
		return []setupcore.Host{setupcore.HostCodex, setupcore.HostClaudeCode}, nil
	}
	host, err := parseSetupHost(args[0])
	if err != nil {
		return nil, err
	}
	return []setupcore.Host{host}, nil
}

func parseSetupHost(value string) (setupcore.Host, error) {
	switch setupcore.Host(value) {
	case setupcore.HostCodex:
		return setupcore.HostCodex, nil
	case setupcore.HostClaudeCode:
		return setupcore.HostClaudeCode, nil
	default:
		return "", usageError(fmt.Errorf("unsupported agent host %q", value))
	}
}

func (a *App) confirmMaintenance(message string, yes bool) error {
	if yes {
		return nil
	}
	if !a.deps.IsTerminal(a.deps.Stderr) {
		return ErrConfirmationRequired
	}
	approved, err := terminal.New(a.deps.Stderr, a.deps.IsTerminal(a.deps.Stderr)).Confirm(a.root.Context(), a.deps.Stdin, "Confirm JetKVM maintenance", message)
	if err != nil {
		return err
	}
	if !approved {
		return ErrConfirmationRequired
	}
	return nil
}

func maximumArgs(count int) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) > count {
			return usageError(fmt.Errorf("accepts at most %d argument(s), received %d", count, len(args)))
		}
		return nil
	}
}
