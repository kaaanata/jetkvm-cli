package setup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"uuid"
)

func (s *Service) Plan(ctx context.Context, request PlanRequest) (Plan, error) {
	request.Target = request.Target.Normalize()
	snapshot, err := s.Inspect(ctx, request.Target, request.PluginVersion)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{
		ID: uuid.NewV7(), Target: request.Target, PluginVersion: request.PluginVersion,
		InitialState: snapshot.State, BeforeFingerprint: snapshot.Fingerprint,
		DryRun: request.DryRun, CreatedAt: s.now(),
	}

	plan.Steps, err = authoritativeSteps(request.Target, snapshot, request.Migrate)
	if err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func authoritativeSteps(target Target, snapshot Snapshot, migrate bool) ([]Step, error) {
	var steps []Step
	switch snapshot.State {
	case StateEquivalent:
		return nil, nil
	case StateForeignConflict:
		return nil, fmt.Errorf("%w: target contains an integration not owned by JetKVM setup", ErrSetupConflict)
	case StateLegacyDirect:
		if !migrate {
			return nil, ErrMigrationNeeded
		}
		steps = append(steps, removeDirectStep(target))
	case StateAbsent, StateOwnedOutdated, StatePartial:
	default:
		return nil, fmt.Errorf("%w: unsupported state %q", ErrSetupConflict, snapshot.State)
	}

	if target.Mode == ModePlugin {
		steps = append(steps, pluginSteps(target, snapshot)...)
	} else {
		steps = append(steps, directSteps(target, snapshot)...)
	}
	return steps, nil
}

func pluginSteps(target Target, snapshot Snapshot) []Step {
	var steps []Step
	if !snapshot.Marketplace.Present {
		steps = append(steps, marketplaceAddStep(target))
	} else if snapshot.State == StateOwnedOutdated {
		steps = append(steps, marketplaceUpgradeStep(target))
	}
	if !snapshot.Plugin.Present {
		steps = append(steps, pluginAddStep(target))
	} else if snapshot.State == StateOwnedOutdated {
		steps = append(steps, pluginUpgradeStep(target))
	}
	return steps
}

func directSteps(target Target, snapshot Snapshot) []Step {
	if snapshot.DirectMCP.Present {
		return nil
	}
	return []Step{directAddStep(target)}
}

func marketplaceAddStep(target Target) Step {
	args := []string{"plugin", "marketplace", "add", MarketplaceSource}
	remove := []string{"plugin", "marketplace", "remove", MarketplaceName}
	if target.Host == HostCodex {
		args = append(args, "--json")
		remove = append(remove, "--json")
	} else {
		args = append(args, "--scope", string(target.Scope))
		remove = append(remove, "--scope", string(target.Scope))
	}
	return Step{Name: "add_marketplace", Do: native(target, args...), Undo: native(target, remove...), Component: "marketplace", Creates: true}
}

func marketplaceUpgradeStep(target Target) Step {
	args := []string{"plugin", "marketplace", "upgrade", MarketplaceName}
	if target.Host == HostClaudeCode {
		args = []string{"plugin", "marketplace", "update", MarketplaceName}
	}
	args = jsonFlag(target, args)
	return Step{Name: "upgrade_marketplace", Do: native(target, args...), Component: "marketplace"}
}

func pluginAddStep(target Target) Step {
	verb := "add"
	undoVerb := "remove"
	if target.Host == HostClaudeCode {
		verb = "install"
		undoVerb = "uninstall"
	}
	args := scopeFlag(target, []string{"plugin", verb, PluginReference})
	undo := scopeFlag(target, []string{"plugin", undoVerb, PluginReference})
	args = jsonFlag(target, args)
	undo = jsonFlag(target, undo)
	return Step{Name: "install_plugin", Do: native(target, args...), Undo: native(target, undo...), Component: "plugin", Creates: true}
}

func pluginUpgradeStep(target Target) Step {
	verb := "upgrade"
	if target.Host == HostClaudeCode {
		verb = "update"
	}
	args := scopeFlag(target, []string{"plugin", verb, PluginReference})
	args = jsonFlag(target, args)
	return Step{Name: "upgrade_plugin", Do: native(target, args...), Component: "plugin"}
}

func directAddStep(target Target) Step {
	args := []string{"mcp", "add"}
	if target.Host == HostClaudeCode {
		args = append(args, "--scope", string(target.Scope))
	}
	args = append(args, "jetkvm", "--", CanonicalMCPCommand)
	args = append(args, canonicalMCPArgs()...)
	remove := []string{"mcp", "remove"}
	if target.Host == HostClaudeCode {
		remove = append(remove, "--scope", string(target.Scope))
	}
	remove = append(remove, "jetkvm")
	return Step{Name: "add_direct_mcp", Do: native(target, args...), Undo: native(target, remove...), Component: "direct_mcp", Creates: true}
}

func removeDirectStep(target Target) Step {
	add := directAddStep(target)
	return Step{Name: "remove_legacy_direct_mcp", Do: add.Undo, Undo: add.Do, Component: "direct_mcp"}
}

func native(target Target, args ...string) Command {
	return Command{Name: hostBinary(target.Host), Args: args, Dir: target.Workspace}
}

func scopeFlag(target Target, args []string) []string {
	if target.Host == HostClaudeCode {
		return append(args, "--scope", string(target.Scope))
	}
	return args
}

func jsonFlag(target Target, args []string) []string {
	if target.Host == HostCodex {
		return append(args, "--json")
	}
	return args
}

func (s *Service) required(ctx context.Context, command Command) (CommandResult, error) {
	result, err := s.runner.Run(ctx, command)
	if err != nil {
		return CommandResult{}, fmt.Errorf("%w: run %s: %w", ErrHostUnavailable, command.Name, err)
	}
	if result.ExitCode != 0 {
		return CommandResult{}, commandError(command, result)
	}
	return result, nil
}

func commandError(command Command, result CommandResult) error {
	detail := strings.TrimSpace(string(result.Stderr))
	if detail == "" {
		detail = strings.TrimSpace(string(result.Stdout))
	}
	if detail == "" {
		detail = "no diagnostic output"
	}
	return fmt.Errorf("%w: %s exited with %d: %s", ErrCommandFailed, command.Name, result.ExitCode, detail)
}

func errorKind(err error) string {
	switch {
	case errors.Is(err, ErrRollbackConflict):
		return "rollback_conflict"
	case errors.Is(err, ErrStalePlan):
		return "stale_plan"
	case errors.Is(err, ErrCommandFailed):
		return "host_command_failed"
	case errors.Is(err, ErrVerification):
		return "verification_failed"
	default:
		return "setup_failed"
	}
}
