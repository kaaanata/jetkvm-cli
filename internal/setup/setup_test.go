package setup

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const testPluginVersion = "1.2.3"

func TestTargetDefaultsAndScopeRules(t *testing.T) {
	t.Parallel()
	workspace := filepath.Join(t.TempDir(), "project")
	tests := []struct {
		name    string
		target  Target
		wantErr bool
	}{
		{name: "default is Codex plugin user", target: Target{Host: HostCodex}},
		{name: "Codex plugin user", target: Target{Host: HostCodex, Mode: ModePlugin, Scope: ScopeUser}},
		{name: "Codex plugin project rejected", target: Target{Host: HostCodex, Mode: ModePlugin, Scope: ScopeProject, Workspace: workspace}, wantErr: true},
		{name: "Codex direct user", target: Target{Host: HostCodex, Mode: ModeDirect, Scope: ScopeUser}},
		{name: "Codex direct project rejected", target: Target{Host: HostCodex, Mode: ModeDirect, Scope: ScopeProject, Workspace: workspace}, wantErr: true},
		{name: "Codex direct local rejected", target: Target{Host: HostCodex, Mode: ModeDirect, Scope: ScopeLocal, Workspace: workspace}, wantErr: true},
		{name: "Claude plugin user", target: Target{Host: HostClaudeCode, Mode: ModePlugin, Scope: ScopeUser}},
		{name: "Claude plugin project", target: Target{Host: HostClaudeCode, Mode: ModePlugin, Scope: ScopeProject, Workspace: workspace}},
		{name: "Claude plugin local", target: Target{Host: HostClaudeCode, Mode: ModePlugin, Scope: ScopeLocal, Workspace: workspace}},
		{name: "Claude direct user", target: Target{Host: HostClaudeCode, Mode: ModeDirect, Scope: ScopeUser}},
		{name: "Claude direct project", target: Target{Host: HostClaudeCode, Mode: ModeDirect, Scope: ScopeProject, Workspace: workspace}},
		{name: "Claude direct local", target: Target{Host: HostClaudeCode, Mode: ModeDirect, Scope: ScopeLocal, Workspace: workspace}},
		{name: "scoped target requires workspace", target: Target{Host: HostClaudeCode, Mode: ModeDirect, Scope: ScopeProject}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := test.target.Normalize()
			err := target.Validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, test.wantErr)
			}
			if test.target.Mode == "" && (target.Mode != ModePlugin || target.Scope != ScopeUser) {
				t.Fatalf("Normalize() = %#v, want plugin/user", target)
			}
		})
	}
}

func TestClassifyExactStates(t *testing.T) {
	t.Parallel()
	pluginTarget := Target{Host: HostCodex, Mode: ModePlugin, Scope: ScopeUser}
	directTarget := Target{Host: HostCodex, Mode: ModeDirect, Scope: ScopeUser}
	ownedMarket := Component{Present: true, Source: MarketplaceSource}
	ownedPlugin := Component{Present: true, Source: MarketplaceName, Version: testPluginVersion}
	direct := Component{Present: true, Command: CanonicalMCPCommand, Args: canonicalMCPArgs()}
	tests := []struct {
		name string
		in   Snapshot
		want State
	}{
		{name: "absent", in: Snapshot{Target: pluginTarget}, want: StateAbsent},
		{name: "equivalent", in: Snapshot{Target: pluginTarget, Marketplace: ownedMarket, Plugin: ownedPlugin}, want: StateEquivalent},
		{name: "owned outdated", in: Snapshot{Target: pluginTarget, Marketplace: ownedMarket, Plugin: Component{Present: true, Source: MarketplaceName, Version: "1.0.0"}}, want: StateOwnedOutdated},
		{name: "foreign conflict", in: Snapshot{Target: pluginTarget, Marketplace: Component{Present: true, Source: "someone/else"}}, want: StateForeignConflict},
		{name: "legacy direct", in: Snapshot{Target: pluginTarget, DirectMCP: direct}, want: StateLegacyDirect},
		{name: "partial", in: Snapshot{Target: pluginTarget, Marketplace: ownedMarket}, want: StatePartial},
		{name: "plugin missing version is partial", in: Snapshot{Target: pluginTarget, Marketplace: ownedMarket, Plugin: Component{Present: true, Source: MarketplaceName}}, want: StatePartial},
		{name: "direct equivalent", in: Snapshot{Target: directTarget, DirectMCP: direct}, want: StateEquivalent},
		{name: "direct rejects plugin coexistence", in: Snapshot{Target: directTarget, Marketplace: ownedMarket, Plugin: ownedPlugin}, want: StateForeignConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classify(test.in, testPluginVersion); got != test.want {
				t.Fatalf("classify() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestParseRealHostPluginJSONShapes(t *testing.T) {
	t.Parallel()
	t.Run("Codex marketplace 0.153.0", func(t *testing.T) {
		fixture := `[{"name":"jetkvm","root":"/cache/jetkvm","marketplaceSource":{"sourceType":"git","source":"kaaanata/jetkvm-cli"}}]`
		component, err := parseMarketplace([]byte(fixture))
		if err != nil || !component.Present || component.Source != MarketplaceSource {
			t.Fatalf("parseMarketplace() = %#v, %v", component, err)
		}
	})

	t.Run("Codex plugin 0.153.0", func(t *testing.T) {
		fixture := `[{"pluginId":"jetkvm@jetkvm","name":"jetkvm","marketplaceName":"jetkvm","version":"1.2.3","source":{"source":"plugins/jetkvm","path":"/cache/plugins/jetkvm"},"marketplaceSource":{"sourceType":"git","source":"kaaanata/jetkvm-cli"}}]`
		component, err := parsePlugin([]byte(fixture))
		if err != nil || !component.Present || component.Source != MarketplaceName || component.Version != testPluginVersion {
			t.Fatalf("parsePlugin() = %#v, %v", component, err)
		}
	})

	t.Run("Claude marketplace 2.1.169", func(t *testing.T) {
		fixture := `[{"name":"jetkvm","source":"github","repo":"kaaanata/jetkvm-cli","installLocation":"/cache/jetkvm"}]`
		component, err := parseMarketplace([]byte(fixture))
		if err != nil || !component.Present || component.Source != MarketplaceSource {
			t.Fatalf("parseMarketplace() = %#v, %v", component, err)
		}
	})

	t.Run("Claude plugin list", func(t *testing.T) {
		component, err := parsePlugin([]byte(`[]`))
		if err != nil || component.Present {
			t.Fatalf("parsePlugin() = %#v, %v", component, err)
		}
	})
}

func TestPlanUsesCanonicalHostNativeCommands(t *testing.T) {
	t.Parallel()
	t.Run("Codex plugin", func(t *testing.T) {
		runner := newFakeRunner()
		service := newTestService(t, runner)
		plan, err := service.Plan(t.Context(), PlanRequest{Target: Target{Host: HostCodex}, PluginVersion: testPluginVersion})
		if err != nil {
			t.Fatal(err)
		}
		want := []Command{
			{Name: "codex", Args: []string{"plugin", "marketplace", "add", MarketplaceSource, "--json"}},
			{Name: "codex", Args: []string{"plugin", "add", PluginReference, "--json"}},
		}
		if got := doCommands(plan.Steps); !slices.EqualFunc(got, want, commandEqual) {
			t.Fatalf("commands = %#v, want %#v", got, want)
		}
		for _, step := range plan.Steps {
			if step.Component == "direct_mcp" {
				t.Fatal("plugin plan silently fell back to direct MCP")
			}
		}
	})

	t.Run("Claude direct project", func(t *testing.T) {
		workspace := filepath.Join(t.TempDir(), "project")
		runner := newFakeRunner()
		service := newTestService(t, runner)
		plan, err := service.Plan(t.Context(), PlanRequest{Target: Target{
			Host: HostClaudeCode, Mode: ModeDirect, Scope: ScopeProject, Workspace: workspace,
		}})
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Steps) != 1 {
			t.Fatalf("steps = %d, want 1", len(plan.Steps))
		}
		want := Command{Name: "claude", Dir: workspace, Args: []string{
			"mcp", "add", "--scope", "project", "jetkvm", "--", "jetkvm", "mcp", "serve", "--transport=stdio",
		}}
		if !commandEqual(plan.Steps[0].Do, want) {
			t.Fatalf("command = %#v, want %#v", plan.Steps[0].Do, want)
		}
	})

	t.Run("Claude plugin help ordering", func(t *testing.T) {
		runner := newFakeRunner()
		service := newTestService(t, runner)
		plan, err := service.Plan(t.Context(), PlanRequest{Target: Target{Host: HostClaudeCode}, PluginVersion: testPluginVersion})
		if err != nil {
			t.Fatal(err)
		}
		want := []Command{
			{Name: "claude", Args: []string{"plugin", "marketplace", "add", MarketplaceSource, "--scope", "user"}},
			{Name: "claude", Args: []string{"plugin", "install", PluginReference, "--scope", "user"}},
		}
		if got := doCommands(plan.Steps); !slices.EqualFunc(got, want, commandEqual) {
			t.Fatalf("commands = %#v, want %#v", got, want)
		}
	})
}

func TestPlanLegacyDirectRequiresExplicitMigration(t *testing.T) {
	t.Parallel()
	runner := newFakeRunner()
	runner.direct = true
	service := newTestService(t, runner)
	target := Target{Host: HostCodex}
	if _, err := service.Plan(t.Context(), PlanRequest{Target: target, PluginVersion: testPluginVersion}); !errors.Is(err, ErrMigrationNeeded) {
		t.Fatalf("Plan() error = %v, want ErrMigrationNeeded", err)
	}
	plan, err := service.Plan(t.Context(), PlanRequest{Target: target, PluginVersion: testPluginVersion, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 3 || plan.Steps[0].Name != "remove_legacy_direct_mcp" {
		t.Fatalf("migration steps = %#v", plan.Steps)
	}
}

func TestApplyPersistsReceiptAndUninstallUsesCAS(t *testing.T) {
	t.Parallel()
	runner := newFakeRunner()
	service := newTestService(t, runner)
	plan, err := service.Plan(t.Context(), PlanRequest{Target: Target{Host: HostCodex}, PluginVersion: testPluginVersion})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := service.Apply(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != ReceiptCommitted || !slices.Equal(receipt.OwnedComponents, []string{"marketplace", "plugin"}) {
		t.Fatalf("receipt = %#v", receipt)
	}
	loaded, err := service.store.Load(receipt.ID)
	if err != nil || loaded.AfterFingerprint != receipt.AfterFingerprint {
		t.Fatalf("loaded receipt = %#v, error = %v", loaded, err)
	}

	runner.pluginVersion = "9.9.9"
	before := runner.mutationCount()
	uninstallReceipt, err := service.Uninstall(t.Context(), plan.Target, false)
	if !errors.Is(err, ErrRollbackConflict) || uninstallReceipt.Status != ReceiptRollbackConflict {
		t.Fatalf("Uninstall() receipt = %#v, error = %v", uninstallReceipt, err)
	}
	if got := runner.mutationCount(); got != before {
		t.Fatalf("CAS conflict executed %d mutation(s)", got-before)
	}
}

func TestApplyRollsBackOnlyObservedOwnedState(t *testing.T) {
	t.Parallel()
	runner := newFakeRunner()
	runner.failPluginAdd = true
	service := newTestService(t, runner)
	plan, err := service.Plan(t.Context(), PlanRequest{Target: Target{Host: HostCodex}, PluginVersion: testPluginVersion})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := service.Apply(t.Context(), plan)
	if !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("Apply() error = %v, want ErrCommandFailed", err)
	}
	if receipt.Status != ReceiptRolledBack || runner.marketplace || runner.plugin {
		t.Fatalf("rollback receipt = %#v, runner = %#v", receipt, runner)
	}
	if len(receipt.RollbackJournal) != 1 || receipt.RollbackJournal[0].Step != "rollback_add_marketplace" {
		t.Fatalf("rollback journal = %#v", receipt.RollbackJournal)
	}
}

func TestApplyRejectsTamperedPlanBeforeMutation(t *testing.T) {
	t.Parallel()
	runner := newFakeRunner()
	service := newTestService(t, runner)
	plan, err := service.Plan(t.Context(), PlanRequest{Target: Target{Host: HostCodex}, PluginVersion: testPluginVersion})
	if err != nil {
		t.Fatal(err)
	}
	plan.Steps[0].Do = Command{Name: "unexpected", Args: []string{"secret-value"}}
	before := runner.mutationCount()
	receipt, err := service.Apply(t.Context(), plan)
	if !errors.Is(err, ErrInvalidPlan) || receipt.Status != ReceiptFailed {
		t.Fatalf("Apply() receipt = %#v, error = %v", receipt, err)
	}
	if got := runner.mutationCount(); got != before {
		t.Fatalf("tampered plan executed %d mutation(s)", got-before)
	}
}

func TestDryRunExecutesNoMutationAndReceiptContainsNoSecrets(t *testing.T) {
	t.Parallel()
	runner := newFakeRunner()
	service := newTestService(t, runner)
	plan, err := service.Plan(t.Context(), PlanRequest{
		Target: Target{Host: HostCodex}, PluginVersion: testPluginVersion, DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	before := runner.mutationCount()
	receipt, err := service.Apply(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != ReceiptDryRun || runner.mutationCount() != before {
		t.Fatalf("dry-run receipt = %#v", receipt)
	}
	encoded, err := json.Marshal(receipt, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret", "token", "credential", "environment"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("receipt contains forbidden field %q: %s", forbidden, encoded)
		}
	}
}

func TestDoctorReportsTerminalState(t *testing.T) {
	t.Parallel()
	runner := newFakeRunner()
	runner.marketplace = true
	runner.plugin = true
	service := newTestService(t, runner)
	report, err := service.Doctor(t.Context(), Target{Host: HostCodex}, testPluginVersion)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != DoctorReady || report.State != StateEquivalent {
		t.Fatalf("Doctor() = %#v", report)
	}
}

func TestTargetLocksSerializeOnlyTheSameTarget(t *testing.T) {
	t.Parallel()
	service := newTestService(t, newFakeRunner())
	target := Target{Host: HostCodex, Mode: ModePlugin, Scope: ScopeUser}
	release, err := service.acquire(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	deadlineContext, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := service.acquire(deadlineContext, target); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("same-target acquire error = %v, want deadline exceeded", err)
	}

	other := Target{Host: HostClaudeCode, Mode: ModePlugin, Scope: ScopeUser}
	releaseOther, err := service.acquire(t.Context(), other)
	if err != nil {
		t.Fatalf("independent target lock: %v", err)
	}
	releaseOther()
}

type fakeRunner struct {
	mu            sync.Mutex
	marketplace   bool
	marketSource  string
	plugin        bool
	pluginSource  string
	pluginVersion string
	direct        bool
	directCommand string
	directArgs    []string
	failPluginAdd bool
	mutations     int
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		marketSource: MarketplaceSource, pluginSource: MarketplaceName,
		pluginVersion: testPluginVersion, directCommand: CanonicalMCPCommand,
		directArgs: canonicalMCPArgs(),
	}
}

func (f *fakeRunner) Run(_ context.Context, command Command) (CommandResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	args := strings.Join(command.Args, " ")
	switch {
	case args == "--version":
		return success("host 1.0.0"), nil
	case args == "plugin marketplace list --json":
		if !f.marketplace {
			return success(`{"marketplaces":[]}`), nil
		}
		return success(fmt.Sprintf(`{"marketplaces":[{"name":"jetkvm","source":%q}]}`, f.marketSource)), nil
	case args == "plugin list --json":
		if !f.plugin {
			return success(`{"plugins":[]}`), nil
		}
		return success(fmt.Sprintf(`{"plugins":[{"id":"jetkvm@jetkvm","source":%q,"version":%q}]}`, f.pluginSource, f.pluginVersion)), nil
	case strings.HasPrefix(args, "mcp get jetkvm"):
		if !f.direct {
			return CommandResult{Stderr: []byte("Error: No MCP server named 'jetkvm' found."), ExitCode: 1}, nil
		}
		if command.Name == "claude" {
			return success("Name: jetkvm\nCommand: " + f.directCommand + "\nArgs: " + strings.Join(f.directArgs, " ")), nil
		}
		encodedArgs, _ := json.Marshal(f.directArgs)
		return success(fmt.Sprintf(`{"name":"jetkvm","command":%q,"args":%s}`, f.directCommand, encodedArgs)), nil
	case strings.HasPrefix(args, "plugin marketplace add "):
		f.marketplace = true
		f.marketSource = MarketplaceSource
		f.mutations++
		return success(`{}`), nil
	case strings.HasPrefix(args, "plugin marketplace remove "):
		f.marketplace = false
		f.mutations++
		return success(`{}`), nil
	case strings.HasPrefix(args, "plugin add ") || strings.HasPrefix(args, "plugin install "):
		if f.failPluginAdd {
			return CommandResult{Stderr: []byte("plugin install failed"), ExitCode: 1}, nil
		}
		f.plugin = true
		f.pluginSource = MarketplaceName
		f.pluginVersion = testPluginVersion
		f.mutations++
		return success(`{}`), nil
	case strings.HasPrefix(args, "plugin remove ") || strings.HasPrefix(args, "plugin uninstall "):
		f.plugin = false
		f.mutations++
		return success(`{}`), nil
	case strings.HasPrefix(args, "mcp add "):
		f.direct = true
		f.directCommand = CanonicalMCPCommand
		f.directArgs = canonicalMCPArgs()
		f.mutations++
		return success(`{}`), nil
	case strings.HasPrefix(args, "mcp remove "):
		f.direct = false
		f.mutations++
		return success(`{}`), nil
	default:
		return CommandResult{Stderr: []byte("unexpected command: " + command.Name + " " + args), ExitCode: 2}, nil
	}
}

func (f *fakeRunner) mutationCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mutations
}

func newTestService(t *testing.T, runner Runner) *Service {
	t.Helper()
	now := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
	service, err := NewService(Config{Runner: runner, StateDir: t.TempDir(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func success(stdout string) CommandResult {
	return CommandResult{Stdout: []byte(stdout)}
}

func doCommands(steps []Step) []Command {
	commands := make([]Command, 0, len(steps))
	for _, step := range steps {
		commands = append(commands, step.Do)
	}
	return commands
}

func commandEqual(left, right Command) bool {
	return left.Name == right.Name && left.Dir == right.Dir && slices.Equal(left.Args, right.Args)
}
