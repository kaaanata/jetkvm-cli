package cli

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/kaaanata/jetkvm-cli/internal/buildinfo"
	setupcore "github.com/kaaanata/jetkvm-cli/internal/setup"
	updatecore "github.com/kaaanata/jetkvm-cli/internal/update"
)

func TestUpdateCheckDoesNotLoadDeviceRuntime(t *testing.T) {
	updater := &fakeUpdateService{check: updatecore.CheckResult{
		Installation: updatecore.Installation{Owner: updatecore.OwnerStandalone, Version: "1.0.0"},
		Release:      updatecore.Release{Version: "1.1.0"}, Available: true,
	}}
	stdout := new(strings.Builder)
	stderr := new(strings.Builder)
	application := New(Dependencies{
		Version: buildinfo.Info{Version: "1.0.0"}, Updater: updater,
		Stdout: stdout, Stderr: stderr, IsTerminal: func(io.Writer) bool { return false },
		Loader: RuntimeLoaderFunc(func(context.Context, string) (Runtime, error) {
			t.Fatal("update loaded the device runtime")
			return Runtime{}, nil
		}),
	})
	if code := application.Execute(t.Context(), []string{"update", "--check"}); code != ExitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout.String(), `"command": "update.check"`) || updater.applyCalls != 0 {
		t.Fatalf("stdout = %s, apply calls = %d", stdout, updater.applyCalls)
	}
}

func TestUpdateRequiresConfirmationBeforeSelfReplace(t *testing.T) {
	updater := &fakeUpdateService{check: updatecore.CheckResult{
		Installation: updatecore.Installation{Owner: updatecore.OwnerStandalone, Version: "1.0.0"},
		Release:      updatecore.Release{Version: "1.1.0"}, Available: true,
	}}
	stderr := new(strings.Builder)
	application := New(Dependencies{
		Version: buildinfo.Info{Version: "1.0.0"}, Updater: updater,
		Stdout: new(strings.Builder), Stderr: stderr, IsTerminal: func(io.Writer) bool { return false },
	})
	if code := application.Execute(t.Context(), []string{"update"}); code != ExitAuth || updater.applyCalls != 0 {
		t.Fatalf("exit = %d, apply calls = %d, stderr = %s", code, updater.applyCalls, stderr)
	}
}

func TestSetupDryRunReturnsAuthoritativePlan(t *testing.T) {
	service := &fakeSetupService{}
	stdout := new(strings.Builder)
	stderr := new(strings.Builder)
	application := New(Dependencies{
		Version: buildinfo.Info{Version: "1.0.0"}, Setup: service,
		Stdout: stdout, Stderr: stderr, IsTerminal: func(io.Writer) bool { return false },
	})
	if code := application.Execute(t.Context(), []string{"setup", "codex", "--dry-run"}); code != ExitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if service.applyCalls != 0 || !strings.Contains(stdout.String(), `"command": "setup.plan"`) || !strings.Contains(stdout.String(), `"add_marketplace"`) {
		t.Fatalf("apply calls = %d, stdout = %s", service.applyCalls, stdout)
	}
}

func TestSetupApplyUsesExplicitNonInteractiveConfirmation(t *testing.T) {
	service := &fakeSetupService{}
	stderr := new(strings.Builder)
	application := New(Dependencies{
		Version: buildinfo.Info{Version: "1.0.0"}, Setup: service,
		Stdout: new(strings.Builder), Stderr: stderr, IsTerminal: func(io.Writer) bool { return false },
	})
	if code := application.Execute(t.Context(), []string{"setup", "claude-code", "--yes"}); code != ExitOK || service.applyCalls != 1 {
		t.Fatalf("exit = %d, apply calls = %d, stderr = %s", code, service.applyCalls, stderr)
	}
}

type fakeUpdateService struct {
	check      updatecore.CheckResult
	applyCalls int
}

func (f *fakeUpdateService) Resolve(_ context.Context, request updatecore.Request) (updatecore.Resolution, error) {
	return updatecore.Resolution{Installation: f.check.Installation, Request: request}, nil
}

func (f *fakeUpdateService) Check(context.Context, updatecore.Resolution) (updatecore.CheckResult, error) {
	return f.check, nil
}

func (*fakeUpdateService) Plan(check updatecore.CheckResult) (updatecore.Plan, error) {
	action := updatecore.ActionNone
	if check.Available {
		action = updatecore.ActionSelfReplace
	}
	return updatecore.Plan{Action: action, Owner: check.Installation.Owner, CurrentVersion: check.Installation.Version, TargetVersion: check.Release.Version}, nil
}

func (f *fakeUpdateService) Apply(context.Context, updatecore.Plan) (updatecore.Result, error) {
	f.applyCalls++
	return updatecore.Result{Status: updatecore.StatusApplied, CurrentVersion: "1.1.0"}, nil
}

func (*fakeUpdateService) Rollback(context.Context) (updatecore.Result, error) {
	return updatecore.Result{Status: updatecore.StatusRolledBack, CurrentVersion: "1.0.0"}, nil
}

type fakeSetupService struct{ applyCalls int }

func (*fakeSetupService) Plan(_ context.Context, request setupcore.PlanRequest) (setupcore.Plan, error) {
	return setupcore.Plan{
		Target: request.Target, PluginVersion: request.PluginVersion, InitialState: setupcore.StateAbsent, DryRun: request.DryRun,
		Steps: []setupcore.Step{{Name: "add_marketplace"}},
	}, nil
}

func (f *fakeSetupService) Apply(_ context.Context, plan setupcore.Plan) (setupcore.Receipt, error) {
	f.applyCalls++
	return setupcore.Receipt{SchemaVersion: 1, Target: plan.Target, Status: setupcore.ReceiptCommitted}, nil
}

func (*fakeSetupService) Uninstall(_ context.Context, target setupcore.Target, _ bool) (setupcore.Receipt, error) {
	return setupcore.Receipt{SchemaVersion: 1, Target: target, Status: setupcore.ReceiptUninstalled}, nil
}

func (*fakeSetupService) Doctor(_ context.Context, target setupcore.Target, _ string) (setupcore.DoctorReport, error) {
	return setupcore.DoctorReport{Target: target, Status: setupcore.DoctorReady, State: setupcore.StateEquivalent}, nil
}

var (
	_ UpdateService = (*fakeUpdateService)(nil)
	_ SetupService  = (*fakeSetupService)(nil)
)
