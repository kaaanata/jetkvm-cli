package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
	"uuid"

	"github.com/gofrs/flock"
	"github.com/kaaanata/jetkvm-cli/internal/progress"
)

const lockRetryInterval = 50 * time.Millisecond

type Config struct {
	Runner   Runner
	StateDir string
	Now      func() time.Time
}

type Service struct {
	runner   Runner
	store    *Store
	locksDir string
	now      func() time.Time
}

func NewService(config Config) (*Service, error) {
	if config.Runner == nil {
		return nil, errors.New("setup runner is required")
	}
	if config.StateDir == "" || !filepath.IsAbs(config.StateDir) {
		return nil, errors.New("setup state directory must be absolute")
	}
	store, err := NewStore(filepath.Join(config.StateDir, "journal"))
	if err != nil {
		return nil, err
	}
	locksDir := filepath.Join(config.StateDir, "locks")
	if err := os.MkdirAll(locksDir, 0o700); err != nil {
		return nil, fmt.Errorf("create setup lock directory: %w", err)
	}
	if err := os.Chmod(locksDir, 0o700); err != nil {
		return nil, fmt.Errorf("protect setup lock directory: %w", err)
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &Service{runner: config.Runner, store: store, locksDir: locksDir, now: now}, nil
}

func (s *Service) Apply(ctx context.Context, plan Plan) (Receipt, error) {
	plan.Target = plan.Target.Normalize()
	if err := plan.Target.Validate(); err != nil {
		return Receipt{}, err
	}
	if plan.ID == uuid.Nil() || plan.BeforeFingerprint == "" {
		return Receipt{}, ErrInvalidPlan
	}
	now := s.now()
	receipt := Receipt{
		SchemaVersion: 1, ID: uuid.NewV7(), PlanID: plan.ID, Target: plan.Target,
		PluginVersion: plan.PluginVersion,
		Status:        ReceiptPrepared, InitialState: plan.InitialState,
		BeforeFingerprint: plan.BeforeFingerprint, CreatedAt: now, UpdatedAt: now,
	}
	if plan.DryRun {
		receipt.Status = ReceiptDryRun
		return receipt, nil
	}

	release, err := s.acquire(ctx, plan.Target)
	if err != nil {
		return Receipt{}, err
	}
	defer release()
	if err := s.store.Save(receipt); err != nil {
		return Receipt{}, err
	}

	current, err := s.Inspect(ctx, plan.Target, plan.PluginVersion)
	if err != nil {
		return s.fail(receipt, err)
	}
	if current.Fingerprint != plan.BeforeFingerprint {
		return s.fail(receipt, ErrStalePlan)
	}
	expectedSteps, err := authoritativeSteps(plan.Target, current, plan.InitialState == StateLegacyDirect)
	if err != nil || !stepsEqual(plan.Steps, expectedSteps) {
		return s.fail(receipt, errors.Join(ErrInvalidPlan, err))
	}
	if len(plan.Steps) == 0 {
		receipt.Status = ReceiptCommitted
		receipt.AfterFingerprint = current.Fingerprint
		receipt.UpdatedAt = s.now()
		return receipt, s.store.Save(receipt)
	}

	for _, step := range plan.Steps {
		progress.Stage(ctx, "Configuring "+string(plan.Target.Host)+": "+step.Name)
		if _, runErr := s.required(ctx, step.Do); runErr != nil {
			return s.rollbackAfterFailure(ctx, plan, receipt, runErr)
		}
		observed, inspectErr := s.Inspect(ctx, plan.Target, plan.PluginVersion)
		if inspectErr != nil {
			return s.rollbackAfterFailure(ctx, plan, receipt, inspectErr)
		}
		receipt.Journal = append(receipt.Journal, JournalEntry{
			Step: step.Name, Command: step.Do, AfterFingerprint: observed.Fingerprint, CompletedAt: s.now(),
		})
		if step.Creates {
			receipt.OwnedComponents = append(receipt.OwnedComponents, step.Component)
		}
		receipt.AfterFingerprint = observed.Fingerprint
		receipt.UpdatedAt = s.now()
		if err := s.store.Save(receipt); err != nil {
			return Receipt{}, err
		}
	}

	after, err := s.Inspect(ctx, plan.Target, plan.PluginVersion)
	if err != nil {
		return s.rollbackAfterFailure(ctx, plan, receipt, err)
	}
	if after.State != StateEquivalent {
		return s.rollbackAfterFailure(ctx, plan, receipt, fmt.Errorf("%w: terminal state is %s", ErrVerification, after.State))
	}
	receipt.Status = ReceiptCommitted
	receipt.AfterFingerprint = after.Fingerprint
	receipt.OwnedComponents = slices.Compact(receipt.OwnedComponents)
	receipt.UpdatedAt = s.now()
	return receipt, s.store.Save(receipt)
}

func (s *Service) Uninstall(ctx context.Context, target Target, dryRun bool) (Receipt, error) {
	target = target.Normalize()
	if err := target.Validate(); err != nil {
		return Receipt{}, err
	}
	owned, err := s.store.LatestOwned(target)
	if err != nil {
		return Receipt{}, err
	}
	now := s.now()
	receipt := Receipt{
		SchemaVersion: 1, ID: uuid.NewV7(), PlanID: owned.PlanID, Target: target,
		PluginVersion: owned.PluginVersion,
		Status:        ReceiptPrepared, InitialState: StateEquivalent,
		BeforeFingerprint: owned.AfterFingerprint, OwnedComponents: slices.Clone(owned.OwnedComponents),
		CreatedAt: now, UpdatedAt: now,
	}
	if dryRun {
		receipt.Status = ReceiptDryRun
		return receipt, nil
	}

	release, err := s.acquire(ctx, target)
	if err != nil {
		return Receipt{}, err
	}
	defer release()
	current, err := s.Inspect(ctx, target, owned.PluginVersion)
	if err != nil {
		return s.fail(receipt, err)
	}
	if current.Fingerprint != owned.AfterFingerprint {
		return s.failAs(receipt, ReceiptRollbackConflict, ErrRollbackConflict)
	}

	for index := len(owned.Journal) - 1; index >= 0; index-- {
		entry := owned.Journal[index]
		step, ok := ownedStep(entry.Step, target)
		if !ok || !step.Creates || !contains(owned.OwnedComponents, step.Component) {
			continue
		}
		observedBefore, err := s.Inspect(ctx, target, owned.PluginVersion)
		if err != nil {
			return s.fail(receipt, err)
		}
		if observedBefore.Fingerprint != current.Fingerprint {
			return s.failAs(receipt, ReceiptRollbackConflict, ErrRollbackConflict)
		}
		progress.Stage(ctx, "Removing integration: "+step.Name)
		if _, err := s.required(ctx, step.Undo); err != nil {
			return s.fail(receipt, err)
		}
		observed, err := s.Inspect(ctx, target, owned.PluginVersion)
		if err != nil {
			return s.fail(receipt, err)
		}
		receipt.RollbackJournal = append(receipt.RollbackJournal, JournalEntry{
			Step: "uninstall_" + step.Name, Command: step.Undo,
			AfterFingerprint: observed.Fingerprint, CompletedAt: s.now(),
		})
		receipt.AfterFingerprint = observed.Fingerprint
		current = observed
	}
	if receipt.AfterFingerprint != owned.BeforeFingerprint {
		return s.failAs(receipt, ReceiptRollbackConflict, ErrRollbackConflict)
	}
	receipt.Status = ReceiptUninstalled
	receipt.UpdatedAt = s.now()
	return receipt, s.store.Save(receipt)
}

func (s *Service) Doctor(ctx context.Context, target Target, version string) (DoctorReport, error) {
	target = target.Normalize()
	snapshot, err := s.Inspect(ctx, target, version)
	if err != nil {
		return DoctorReport{}, err
	}
	report := DoctorReport{Target: target, State: snapshot.State, Observed: s.now()}
	report.Checks = append(report.Checks,
		DoctorCheck{Name: "host", OK: snapshot.HostVersion != "", Message: snapshot.HostVersion},
		DoctorCheck{Name: "ownership", OK: snapshot.State != StateForeignConflict, Message: string(snapshot.State)},
		DoctorCheck{Name: "integration", OK: snapshot.State == StateEquivalent, Message: string(snapshot.State)},
	)
	switch snapshot.State {
	case StateEquivalent:
		report.Status = DoctorReady
	case StateForeignConflict:
		report.Status = DoctorConflict
	default:
		report.Status = DoctorActionRequired
	}
	return report, nil
}

func (s *Service) rollbackAfterFailure(ctx context.Context, plan Plan, receipt Receipt, cause error) (Receipt, error) {
	if len(receipt.Journal) == 0 {
		return s.fail(receipt, cause)
	}
	current, err := s.Inspect(ctx, plan.Target, plan.PluginVersion)
	if err != nil || current.Fingerprint != receipt.Journal[len(receipt.Journal)-1].AfterFingerprint {
		return s.failAs(receipt, ReceiptRollbackConflict, errors.Join(cause, ErrRollbackConflict, err))
	}
	for index := len(receipt.Journal) - 1; index >= 0; index-- {
		entry := receipt.Journal[index]
		step, ok := stepByName(plan.Steps, entry.Step)
		if !ok || step.Undo.Name == "" {
			continue
		}
		observedBefore, inspectErr := s.Inspect(ctx, plan.Target, plan.PluginVersion)
		if inspectErr != nil || observedBefore.Fingerprint != current.Fingerprint {
			return s.failAs(receipt, ReceiptRollbackConflict, errors.Join(cause, ErrRollbackConflict, inspectErr))
		}
		if _, err := s.required(ctx, step.Undo); err != nil {
			return s.failAs(receipt, ReceiptFailed, errors.Join(cause, err))
		}
		observed, err := s.Inspect(ctx, plan.Target, plan.PluginVersion)
		if err != nil {
			return s.failAs(receipt, ReceiptFailed, errors.Join(cause, err))
		}
		receipt.RollbackJournal = append(receipt.RollbackJournal, JournalEntry{
			Step: "rollback_" + step.Name, Command: step.Undo,
			AfterFingerprint: observed.Fingerprint, CompletedAt: s.now(),
		})
		receipt.AfterFingerprint = observed.Fingerprint
		current = observed
	}
	if receipt.AfterFingerprint != plan.BeforeFingerprint {
		return s.failAs(receipt, ReceiptRollbackConflict, errors.Join(cause, ErrRollbackConflict))
	}
	receipt.Status = ReceiptRolledBack
	receipt.FailureKind = errorKind(cause)
	receipt.UpdatedAt = s.now()
	if err := s.store.Save(receipt); err != nil {
		return Receipt{}, err
	}
	return receipt, cause
}

func (s *Service) fail(receipt Receipt, cause error) (Receipt, error) {
	return s.failAs(receipt, ReceiptFailed, cause)
}

func (s *Service) failAs(receipt Receipt, status ReceiptStatus, cause error) (Receipt, error) {
	receipt.Status = status
	receipt.FailureKind = errorKind(cause)
	receipt.UpdatedAt = s.now()
	if err := s.store.Save(receipt); err != nil {
		return Receipt{}, errors.Join(cause, err)
	}
	return receipt, cause
}

func (s *Service) acquire(ctx context.Context, target Target) (func(), error) {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s", target.Host, target.Mode, target.Scope, target.Workspace)))
	path := filepath.Join(s.locksDir, hex.EncodeToString(digest[:])+".lock")
	fileLock := flock.New(path, flock.SetPermissions(0o600))
	locked, err := fileLock.TryLockContext(ctx, lockRetryInterval)
	if err != nil {
		return nil, fmt.Errorf("acquire setup target lock: %w", err)
	}
	if !locked {
		return nil, context.Cause(ctx)
	}
	return func() { _ = fileLock.Unlock() }, nil
}

func stepByName(steps []Step, name string) (Step, bool) {
	for _, step := range steps {
		if step.Name == name {
			return step, true
		}
	}
	return Step{}, false
}

func ownedStep(name string, target Target) (Step, bool) {
	switch name {
	case "add_marketplace":
		return marketplaceAddStep(target), true
	case "install_plugin":
		return pluginAddStep(target), true
	case "add_direct_mcp":
		return directAddStep(target), true
	default:
		return Step{}, false
	}
}

func stepsEqual(left, right []Step) bool {
	return slices.EqualFunc(left, right, func(leftStep, rightStep Step) bool {
		return leftStep.Name == rightStep.Name && leftStep.Component == rightStep.Component &&
			leftStep.Creates == rightStep.Creates && setupCommandEqual(leftStep.Do, rightStep.Do) &&
			setupCommandEqual(leftStep.Undo, rightStep.Undo)
	})
}

func setupCommandEqual(left, right Command) bool {
	return left.Name == right.Name && left.Dir == right.Dir && slices.Equal(left.Args, right.Args)
}
