package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestResolveNormalizesVersionAndChannel(t *testing.T) {
	service := newTestService(t, OwnerStandalone, "2.0.0")
	resolution, err := service.Resolve(t.Context(), Request{Version: "1.5.0", AllowDowngrade: true})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Request.Version != "v1.5.0" || resolution.Request.Channel != ChannelStable {
		t.Fatalf("unexpected normalized request: %+v", resolution.Request)
	}
}

func TestRequestRules(t *testing.T) {
	tests := []Request{
		{Channel: "nightly"},
		{AllowDowngrade: true},
		{Version: "latest"},
	}
	for _, request := range tests {
		_, err := normalizeRequest(request)
		if kindOf(err) != ErrInvalidRequest {
			t.Fatalf("normalizeRequest(%+v) kind = %q, want %q", request, kindOf(err), ErrInvalidRequest)
		}
	}
}

func TestCheckReleaseSelectionRules(t *testing.T) {
	backend := &fakeBackend{release: Release{Version: "2.0.0-beta.1", Prerelease: true}}
	service := newTestServiceWithBackend(t, OwnerStandalone, "1.0.0", backend)

	resolution, err := service.Resolve(t.Context(), Request{Channel: ChannelPrerelease})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Check(t.Context(), resolution); err != nil {
		t.Fatal(err)
	}
	if !backend.lastQuery.Prerelease || backend.lastQuery.Version != "" {
		t.Fatalf("unexpected prerelease query: %+v", backend.lastQuery)
	}

	resolution, err = service.Resolve(t.Context(), Request{Version: "v2.0.0-beta.1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Check(t.Context(), resolution); err != nil {
		t.Fatal(err)
	}
	if backend.lastQuery.Version != "v2.0.0-beta.1" || !backend.lastQuery.Prerelease {
		t.Fatalf("unexpected exact query: %+v", backend.lastQuery)
	}
}

func TestCheckRejectsBackendViolatingChannelOrExactVersion(t *testing.T) {
	backend := &fakeBackend{release: Release{Version: "2.0.0-beta.1", Prerelease: true}}
	service := newTestServiceWithBackend(t, OwnerStandalone, "1.0.0", backend)
	resolution, err := service.Resolve(t.Context(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Check(t.Context(), resolution); kindOf(err) != ErrReleaseResolution {
		t.Fatalf("stable prerelease kind = %q", kindOf(err))
	}

	backend.release = Release{Version: "2.0.0"}
	resolution, err = service.Resolve(t.Context(), Request{Version: "1.5.0"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Check(t.Context(), resolution); kindOf(err) != ErrReleaseNotFound {
		t.Fatalf("exact mismatch kind = %q", kindOf(err))
	}
}

func TestDowngradeRequiresExactVersionAndOptIn(t *testing.T) {
	backend := &fakeBackend{release: Release{Version: "1.5.0"}}
	service := newTestServiceWithBackend(t, OwnerStandalone, "2.0.0", backend)
	resolution, err := service.Resolve(t.Context(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Check(t.Context(), resolution)
	if kindOf(err) != ErrInvalidRequest {
		t.Fatalf("kind = %q, want %q", kindOf(err), ErrInvalidRequest)
	}

	resolution, err = service.Resolve(t.Context(), Request{Version: "1.0.0", AllowDowngrade: true})
	if err != nil {
		t.Fatal(err)
	}
	backend.release = Release{Version: "1.0.0"}
	check, err := service.Check(t.Context(), resolution)
	if err != nil {
		t.Fatal(err)
	}
	if !check.Downgrade || !check.Available {
		t.Fatalf("unexpected downgrade check: %+v", check)
	}
}

func TestPlanOwnerMatrix(t *testing.T) {
	managerOwners := []Owner{OwnerHomebrew, OwnerWinget, OwnerScoop, OwnerDeb, OwnerRPM}
	for _, owner := range managerOwners {
		t.Run(string(owner), func(t *testing.T) {
			service := newTestService(t, owner, "1.0.0")
			check := testCheck(t, service)
			plan, err := service.Plan(check)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Action != ActionRequired || len(plan.Command) == 0 {
				t.Fatalf("unexpected plan: %+v", plan)
			}
		})
	}

	for _, owner := range []Owner{OwnerSource, OwnerUnmanaged, OwnerUnknown} {
		t.Run(string(owner), func(t *testing.T) {
			service := newTestService(t, owner, "1.0.0")
			_, err := service.Plan(testCheck(t, service))
			if kindOf(err) != ErrUnsupportedOwner {
				t.Fatalf("kind = %q, want %q", kindOf(err), ErrUnsupportedOwner)
			}
		})
	}
}

func TestApplyAndRollbackCommitBinaryAndReceipt(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "jetkvm")
	if err := os.WriteFile(executable, []byte("one"), 0o755); err != nil {
		t.Fatal(err)
	}
	receipts := FileReceiptStore{}
	receipt := mustReceipt(t, executable, "1.0.0")
	if err := receipts.Save(receipt); err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{release: Release{Version: "2.0.0"}, nextBinary: []byte("two")}
	service, err := NewService(
		PortableInstallationResolver{Executable: executable, Receipts: receipts},
		backend, receipts, nopLocker{},
	)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC) }

	result, err := service.Apply(t.Context(), mustPlan(t, service))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusApplied || !result.RollbackAvailable || !result.Verified {
		t.Fatalf("unexpected apply result: %+v", result)
	}
	assertFile(t, executable, "two")
	updated, err := receipts.Load(executable)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != "2.0.0" || updated.InstallID != receipt.InstallID {
		t.Fatalf("unexpected updated receipt: %+v", updated)
	}

	result, err = service.Rollback(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusRolledBack || result.CurrentVersion != "1.0.0" {
		t.Fatalf("unexpected rollback result: %+v", result)
	}
	assertFile(t, executable, "one")
	restored, err := receipts.Load(executable)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Version != "1.0.0" || restored.InstallID != receipt.InstallID {
		t.Fatalf("unexpected restored receipt: %+v", restored)
	}
}

func TestApplyRestoresBinaryWhenReceiptCommitFails(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "jetkvm")
	if err := os.WriteFile(executable, []byte("one"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := FileReceiptStore{}
	if err := base.Save(mustReceipt(t, executable, "1.0.0")); err != nil {
		t.Fatal(err)
	}
	receipts := &failingReceiptStore{ReceiptStore: base, failVersion: "2.0.0"}
	backend := &fakeBackend{release: Release{Version: "2.0.0"}, nextBinary: []byte("two")}
	service, err := NewService(
		PortableInstallationResolver{Executable: executable, Receipts: receipts},
		backend, receipts, nopLocker{},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Apply(t.Context(), mustPlan(t, service))
	if kindOf(err) != ErrApplyFailed {
		t.Fatalf("kind = %q, want %q", kindOf(err), ErrApplyFailed)
	}
	assertFile(t, executable, "one")
	receipt, loadErr := base.Load(executable)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if receipt.Version != "1.0.0" {
		t.Fatalf("receipt version = %q, want 1.0.0", receipt.Version)
	}
}

func TestUnmanagedWithoutReceiptCannotUpdate(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "jetkvm")
	resolver := PortableInstallationResolver{
		Executable: executable, Receipts: FileReceiptStore{}, MissingOwner: OwnerUnmanaged,
		Version: "1.0.0", Channel: ChannelStable,
	}
	service, err := NewService(resolver, &fakeBackend{release: Release{Version: "2.0.0"}}, FileReceiptStore{}, nopLocker{})
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := service.Resolve(t.Context(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	check, err := service.Check(t.Context(), resolution)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Plan(check)
	if kindOf(err) != ErrUnsupportedOwner {
		t.Fatalf("kind = %q, want %q", kindOf(err), ErrUnsupportedOwner)
	}
	if _, statErr := os.Stat(ReceiptPath(executable)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unmanaged resolver created a receipt: %v", statErr)
	}
}

func TestManagerApplyReturnsActionWithoutExecuting(t *testing.T) {
	service := newTestService(t, OwnerHomebrew, "1.0.0")
	plan, err := service.Plan(testCheck(t, service))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"brew", "upgrade", "kaaanata/tap/jetkvm"}
	if result.Status != StatusActionRequired || !slices.Equal(result.ActionRequired, want) {
		t.Fatalf("unexpected result: %+v", result)
	}
}

type staticInstallationResolver struct{ installation Installation }

func (r staticInstallationResolver) ResolveInstallation(context.Context) (Installation, error) {
	return r.installation, nil
}

type nopLocker struct{}

func (nopLocker) Lock(context.Context) (func() error, error) { return func() error { return nil }, nil }

type fakeBackend struct {
	applyErr   error
	release    Release
	lastQuery  ReleaseQuery
	nextBinary []byte
}

func (b *fakeBackend) Resolve(_ context.Context, query ReleaseQuery) (Release, error) {
	b.lastQuery = query
	return b.release, nil
}

func (b *fakeBackend) Apply(_ context.Context, _ Release, target, backup string) error {
	if b.applyErr != nil {
		return b.applyErr
	}
	current, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	if err := os.WriteFile(backup, current, 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, b.nextBinary, 0o755)
}

func TestApplyArchiveRejectionPreservesCauseAndInstallation(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "jetkvm")
	if err := os.WriteFile(executable, []byte("original"), 0o755); err != nil {
		t.Fatal(err)
	}
	receipts := FileReceiptStore{}
	receipt := mustReceipt(t, executable, "1.0.0")
	if err := receipts.Save(receipt); err != nil {
		t.Fatal(err)
	}
	cause := newError(ErrApplyFailed, "release archive must contain exactly four files")
	backend := &fakeBackend{release: Release{Version: "1.0.3"}, applyErr: cause}
	service, err := NewService(PortableInstallationResolver{Executable: executable, Receipts: receipts}, backend, receipts, nopLocker{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Apply(t.Context(), mustPlan(t, service))
	typed, ok := errors.AsType[*Error](err)
	if !ok || typed.Kind != ErrApplyFailed || typed.Message != "apply or activate verified release failed; installation unchanged" || !errors.Is(err, cause) {
		t.Fatalf("unexpected error: %v", err)
	}
	assertFile(t, executable, "original")
	after, err := receipts.Load(executable)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != receipt.Version || after.InstallID != receipt.InstallID {
		t.Fatal("receipt changed before activation")
	}
	if _, err := os.Stat(previousBinaryPath(executable)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected activation backup: %v", err)
	}
}

func (b *fakeBackend) ReplaceFromFile(_ context.Context, source, target, backup string) error {
	replacement, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	current, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	if err := os.WriteFile(backup, current, 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, replacement, 0o755)
}

type failingReceiptStore struct {
	ReceiptStore
	failVersion string
}

func (s *failingReceiptStore) Save(receipt InstallReceipt) error {
	if receipt.Version == s.failVersion {
		return errors.New("injected receipt failure")
	}
	return s.ReceiptStore.Save(receipt)
}

func newTestService(t *testing.T, owner Owner, current string) *Service {
	t.Helper()
	return newTestServiceWithBackend(t, owner, current, &fakeBackend{release: Release{Version: "1.5.0"}})
}

func newTestServiceWithBackend(t *testing.T, owner Owner, current string, backend Backend) *Service {
	t.Helper()
	service, err := NewService(staticInstallationResolver{installation: Installation{
		Owner: owner, Executable: "/opt/jetkvm", Version: current,
		Repository: Repository, Channel: ChannelStable, InstallID: "018f3f6a-7c31-7b55-9d22-9b2d756210a4",
	}}, backend, FileReceiptStore{}, nopLocker{})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testCheck(t *testing.T, service *Service) CheckResult {
	t.Helper()
	resolution, err := service.Resolve(t.Context(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	check, err := service.Check(t.Context(), resolution)
	if err != nil {
		t.Fatal(err)
	}
	return check
}

func mustPlan(t *testing.T, service *Service) Plan {
	t.Helper()
	plan, err := service.Plan(testCheck(t, service))
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func mustReceipt(t *testing.T, executable, version string) InstallReceipt {
	t.Helper()
	receipt, err := NewStandaloneReceipt(executable, version, ChannelStable, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}

func kindOf(err error) ErrorKind {
	if typed, ok := errors.AsType[*Error](err); ok {
		return typed.Kind
	}
	return ""
}
