package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
)

type InstallationResolver interface {
	ResolveInstallation(context.Context) (Installation, error)
}

type PortableInstallationResolver struct {
	Executable   string
	Receipts     ReceiptStore
	MissingOwner Owner
	Version      string
	Channel      Channel
}

func (r PortableInstallationResolver) ResolveInstallation(context.Context) (Installation, error) {
	receipts := r.Receipts
	if receipts == nil {
		receipts = FileReceiptStore{}
	}
	receipt, err := receipts.Load(r.Executable)
	if err == nil {
		return receipt.Installation(), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Installation{}, err
	}
	owner := r.MissingOwner
	if owner == "" {
		owner = OwnerUnknown
	}
	abs, absErr := filepath.Abs(r.Executable)
	if absErr != nil {
		return Installation{}, absErr
	}
	return Installation{
		Owner: owner, Executable: filepath.Clean(abs), Version: strings.TrimPrefix(r.Version, "v"),
		Repository: Repository, Channel: r.Channel,
	}, nil
}

type Service struct {
	installations InstallationResolver
	backend       Backend
	receipts      ReceiptStore
	locker        Locker
	now           func() time.Time
}

func NewService(installations InstallationResolver, backend Backend, receipts ReceiptStore, locker Locker) (*Service, error) {
	if installations == nil || backend == nil || receipts == nil || locker == nil {
		return nil, newError(ErrInvalidRequest, "installation resolver, backend, receipts, and locker are required")
	}
	return &Service{installations: installations, backend: backend, receipts: receipts, locker: locker, now: time.Now}, nil
}

func (s *Service) Resolve(ctx context.Context, request Request) (Resolution, error) {
	request, err := normalizeRequest(request)
	if err != nil {
		return Resolution{}, err
	}
	installation, err := s.installations.ResolveInstallation(ctx)
	if err != nil {
		return Resolution{}, err
	}
	if !installation.Owner.Valid() {
		return Resolution{}, newError(ErrUnsupportedOwner, "invalid installation owner %q", installation.Owner)
	}
	if _, err := semver.StrictNewVersion(installation.Version); err != nil {
		return Resolution{}, newError(ErrInvalidReceipt, "current version must be strict semantic version: %v", err)
	}
	return Resolution{Installation: installation, Request: request}, nil
}

func (s *Service) Check(ctx context.Context, resolution Resolution) (CheckResult, error) {
	query := ReleaseQuery{Version: resolution.Request.Version, Prerelease: resolution.Request.Channel == ChannelPrerelease}
	release, err := s.backend.Resolve(ctx, query)
	if err != nil {
		return CheckResult{}, err
	}
	current, _ := semver.StrictNewVersion(resolution.Installation.Version)
	target, err := semver.StrictNewVersion(release.Version)
	if err != nil {
		return CheckResult{}, newError(ErrReleaseNotFound, "release version is not strict semantic version: %v", err)
	}
	if resolution.Request.Version != "" {
		exact, _ := semver.StrictNewVersion(strings.TrimPrefix(resolution.Request.Version, "v"))
		if !target.Equal(exact) {
			return CheckResult{}, newError(ErrReleaseNotFound, "resolved release %q does not match exact version %q", release.Version, resolution.Request.Version)
		}
	} else if resolution.Request.Channel == ChannelStable && release.Prerelease {
		return CheckResult{}, newError(ErrReleaseResolution, "stable channel resolved a prerelease")
	}
	comparison := target.Compare(current)
	downgrade := comparison < 0
	if downgrade && (!resolution.Request.AllowDowngrade || resolution.Request.Version == "") {
		return CheckResult{}, newError(ErrInvalidRequest, "downgrade requires an exact version and allow_downgrade")
	}
	return CheckResult{
		Installation: resolution.Installation, Request: resolution.Request, Release: release,
		Available: comparison != 0, Downgrade: downgrade,
	}, nil
}

func (s *Service) Plan(check CheckResult) (Plan, error) {
	plan := Plan{
		Action: ActionNone, Owner: check.Installation.Owner,
		CurrentVersion: check.Installation.Version, TargetVersion: check.Release.Version,
		Executable: check.Installation.Executable, Channel: check.Request.Channel,
		Release: check.Release, InstallID: check.Installation.InstallID,
	}
	if !check.Available {
		return plan, nil
	}
	switch check.Installation.Owner {
	case OwnerStandalone:
		plan.Action = ActionSelfReplace
	case OwnerHomebrew, OwnerWinget, OwnerScoop, OwnerDeb, OwnerRPM:
		plan.Action = ActionRequired
		plan.Command = managerCommand(check.Installation.Owner)
	case OwnerSource, OwnerUnmanaged, OwnerUnknown:
		return Plan{}, &Error{Kind: ErrUnsupportedOwner, Owner: check.Installation.Owner, Message: fmt.Sprintf("%s installations cannot self-update", check.Installation.Owner)}
	default:
		return Plan{}, newError(ErrUnsupportedOwner, "invalid installation owner %q", check.Installation.Owner)
	}
	return plan, nil
}

func (s *Service) Apply(ctx context.Context, plan Plan) (Result, error) {
	if plan.Action == ActionNone {
		return Result{Status: StatusUpToDate, Owner: plan.Owner, CurrentVersion: plan.CurrentVersion}, nil
	}
	if plan.Action == ActionRequired {
		return Result{Status: StatusActionRequired, Owner: plan.Owner, CurrentVersion: plan.CurrentVersion, ActionRequired: plan.Command}, nil
	}
	if plan.Action != ActionSelfReplace || plan.Owner != OwnerStandalone {
		return Result{}, newError(ErrUnsupportedOwner, "only standalone installations can replace the executable")
	}

	unlock, err := s.locker.Lock(ctx)
	if err != nil {
		return Result{}, err
	}
	defer unlock()

	current, err := s.receipts.Load(plan.Executable)
	if err != nil {
		return Result{}, err
	}
	if current.Owner != OwnerStandalone || current.InstallID != plan.InstallID || current.Version != plan.CurrentVersion {
		return Result{}, newError(ErrReceiptMismatch, "install receipt changed after update planning")
	}
	if err := s.receipts.SavePrevious(current); err != nil {
		return Result{}, fmt.Errorf("save rollback receipt: %w", err)
	}

	backup := previousBinaryPath(plan.Executable)
	if err := s.backend.Apply(ctx, plan.Release, plan.Executable, backup); err != nil {
		if _, statErr := os.Stat(backup); statErr == nil {
			failed := failedBinaryPath(plan.Executable)
			rollbackErr := s.backend.ReplaceFromFile(ctx, backup, plan.Executable, failed)
			if rollbackErr != nil {
				return Result{}, &Error{Kind: ErrRollbackFailed, Message: "update activation failed and executable rollback failed", Cause: errors.Join(err, rollbackErr)}
			}
			_ = os.Remove(failed)
			_ = os.Remove(backup)
		}
		_ = s.receipts.RemovePrevious(plan.Executable)
		if typed, ok := errors.AsType[*Error](err); ok && (typed.Kind == ErrChecksumMismatch || typed.Kind == ErrSignatureVerification) {
			return Result{}, typed
		}
		return Result{}, &Error{Kind: ErrApplyFailed, Message: "apply or activate verified release; executable restored", Cause: err}
	}

	next := current
	next.Version = plan.TargetVersion
	next.Channel = plan.Channel
	next.InstalledAt = s.now().UTC()
	if err := s.receipts.Save(next); err != nil {
		failed := failedBinaryPath(plan.Executable)
		rollbackErr := s.backend.ReplaceFromFile(ctx, backup, plan.Executable, failed)
		if rollbackErr == nil {
			_ = os.Remove(failed)
			_ = os.Remove(backup)
			_ = s.receipts.RemovePrevious(plan.Executable)
			return Result{}, &Error{Kind: ErrApplyFailed, Message: "commit updated install receipt; executable restored", Cause: err}
		}
		return Result{}, &Error{Kind: ErrRollbackFailed, Message: "commit receipt failed and executable rollback failed", Cause: errors.Join(err, rollbackErr)}
	}

	return Result{
		Status: StatusApplied, Owner: OwnerStandalone, PreviousVersion: current.Version,
		CurrentVersion: next.Version, Verified: true, RollbackAvailable: true,
	}, nil
}

func (s *Service) Rollback(ctx context.Context) (Result, error) {
	installation, err := s.installations.ResolveInstallation(ctx)
	if err != nil {
		return Result{}, err
	}
	if installation.Owner != OwnerStandalone {
		return Result{}, &Error{Kind: ErrUnsupportedOwner, Owner: installation.Owner, Message: "only standalone installations can roll back"}
	}
	unlock, err := s.locker.Lock(ctx)
	if err != nil {
		return Result{}, err
	}
	defer unlock()

	current, err := s.receipts.Load(installation.Executable)
	if err != nil {
		return Result{}, err
	}
	previous, err := s.receipts.LoadPrevious(installation.Executable)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, newError(ErrRollbackUnavailable, "no verified rollback is available")
		}
		return Result{}, err
	}
	if current.InstallID != previous.InstallID || current.Executable != previous.Executable || previous.Owner != OwnerStandalone {
		return Result{}, newError(ErrReceiptMismatch, "rollback receipt does not belong to this installation")
	}

	backup := previousBinaryPath(installation.Executable)
	failed := failedBinaryPath(installation.Executable)
	if err := s.backend.ReplaceFromFile(ctx, backup, installation.Executable, failed); err != nil {
		return Result{}, &Error{Kind: ErrRollbackFailed, Message: "restore previous executable", Cause: err}
	}
	if err := s.receipts.Save(previous); err != nil {
		restoreErr := s.backend.ReplaceFromFile(ctx, failed, installation.Executable, backup)
		return Result{}, &Error{Kind: ErrRollbackFailed, Message: "restore previous receipt", Cause: errors.Join(err, restoreErr)}
	}
	_ = os.Remove(backup)
	_ = os.Remove(failed)
	_ = s.receipts.RemovePrevious(installation.Executable)
	return Result{
		Status: StatusRolledBack, Owner: OwnerStandalone, PreviousVersion: current.Version,
		CurrentVersion: previous.Version, Verified: true,
	}, nil
}

func normalizeRequest(request Request) (Request, error) {
	if request.Channel == "" {
		request.Channel = ChannelStable
	}
	if request.Channel != ChannelStable && request.Channel != ChannelPrerelease {
		return Request{}, newError(ErrInvalidRequest, "channel must be stable or prerelease")
	}
	if request.AllowDowngrade && request.Version == "" {
		return Request{}, newError(ErrInvalidRequest, "allow_downgrade requires an exact version")
	}
	if request.Version != "" {
		request.Version = strings.TrimPrefix(request.Version, "v")
		version, err := semver.StrictNewVersion(request.Version)
		if err != nil {
			return Request{}, newError(ErrInvalidRequest, "version must be strict semantic version: %v", err)
		}
		request.Version = "v" + version.String()
		if version.Prerelease() != "" {
			request.Channel = ChannelPrerelease
		}
	}
	return request, nil
}

func managerCommand(owner Owner) []string {
	switch owner {
	case OwnerHomebrew:
		return []string{"brew", "upgrade", "kaaanata/tap/jetkvm"}
	case OwnerWinget:
		return []string{"winget", "upgrade", "--id", "kaaanata.jetkvm", "--exact"}
	case OwnerScoop:
		return []string{"scoop", "update", "jetkvm"}
	case OwnerDeb:
		return []string{"sudo", "apt-get", "install", "--only-upgrade", "jetkvm"}
	case OwnerRPM:
		return []string{"sudo", "dnf", "upgrade", "jetkvm"}
	default:
		return nil
	}
}

func previousBinaryPath(executable string) string { return executable + ".previous" }
func failedBinaryPath(executable string) string   { return executable + ".failed" }
