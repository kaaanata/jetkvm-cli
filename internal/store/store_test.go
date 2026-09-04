package store

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/operation"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error: %v", err)
		}
	})
	return store
}

func newTestRequest(t *testing.T, id uuid.UUID, deviceID domain.DeviceID, generation uint64, effect operation.EffectClass, action, policyRevision string, canonicalArguments []byte) operation.Request {
	t.Helper()
	digester, err := operation.NewDigester([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewDigester() error: %v", err)
	}
	request, err := digester.NewRequest(id, deviceID, generation, effect, action, policyRevision, canonicalArguments)
	if err != nil {
		t.Fatalf("NewRequest() error: %v", err)
	}
	return request
}

func TestIdentityPinning(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 9, 5, 2, 3, 4, 567_000_000, time.UTC)
	pin := IdentityPin{Alias: "lab", Origin: "https://jetkvm.local", DeviceID: domain.DeviceID("stable-1")}

	stored, existing, err := store.PinIdentity(t.Context(), pin, now)
	if err != nil || existing {
		t.Fatalf("first PinIdentity() = (%+v, %v, %v), want new pin", stored, existing, err)
	}
	if stored.DeviceID != pin.DeviceID || !stored.CreatedAt.Equal(now) {
		t.Fatalf("stored pin = %+v", stored)
	}
	_, existing, err = store.PinIdentity(t.Context(), pin, now.Add(time.Hour))
	if err != nil || !existing {
		t.Fatalf("repeated PinIdentity() existing=%v error=%v", existing, err)
	}
	if _, _, err := store.PinIdentity(t.Context(), IdentityPin{
		Alias: "lab", Origin: pin.Origin, DeviceID: domain.DeviceID("other"),
	}, now); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("identity replacement error = %v, want ErrIdentityConflict", err)
	}
	if _, err := store.VerifyIdentity(t.Context(), "lab", pin.Origin, domain.DeviceID("other")); !errors.Is(err, domain.ErrDeviceIdentityMismatch) {
		t.Fatalf("VerifyIdentity() error = %v, want identity mismatch", err)
	}
	if _, err := store.VerifyIdentity(t.Context(), "lab", pin.Origin, pin.DeviceID); err != nil {
		t.Fatalf("VerifyIdentity() unexpected error: %v", err)
	}
}

func TestOperationDedupConflictAndRecovery(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 9, 5, 3, 0, 0, 0, time.UTC)
	id := uuid.New()
	request := newTestRequest(t, id, domain.DeviceID("stable-1"), 3, operation.EffectPower, "reset", "policy-1", []byte(`{"action":"reset"}`))

	receipt, existing, err := store.Begin(t.Context(), request, now)
	if err != nil || existing || receipt.Stage != operation.StageNotSent {
		t.Fatalf("Begin() = (%+v, %v, %v)", receipt, existing, err)
	}
	receipt, existing, err = store.Begin(t.Context(), request, now.Add(time.Second))
	if err != nil || !existing || !receipt.CreatedAt.Equal(now) {
		t.Fatalf("deduplicated Begin() = (%+v, %v, %v)", receipt, existing, err)
	}
	conflict := newTestRequest(t, id, request.DeviceID, request.ControlGeneration, request.Effect, request.Action, request.PolicyRevision, []byte(`{"action":"hold"}`))
	if _, _, err := store.Begin(t.Context(), conflict, now); !errors.Is(err, operation.ErrConflict) {
		t.Fatalf("conflicting Begin() error = %v, want ErrConflict", err)
	}

	receipt, err = store.Transition(t.Context(), id, operation.StageSendStarted, operation.Patch{
		Delivery:      operation.DeliveryPossiblySent,
		Verification:  operation.Verification{Status: operation.VerificationNotRequested},
		TerminalClaim: operation.TerminalClaimNotProven,
	}, now.Add(time.Second))
	if err != nil || receipt.Stage != operation.StageSendStarted || receipt.SendStartedAt.IsZero() {
		t.Fatalf("send_started transition = (%+v, %v)", receipt, err)
	}
	recovered, err := store.RecoverInterrupted(t.Context(), now.Add(2*time.Second))
	if err != nil || recovered != 1 {
		t.Fatalf("RecoverInterrupted() = (%d, %v), want (1, nil)", recovered, err)
	}
	receipt, err = store.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if receipt.Stage != operation.StageAmbiguous || receipt.Delivery != operation.DeliveryPossiblySent || receipt.RetrySafe || !receipt.IsTerminal() {
		t.Fatalf("recovered receipt = %+v", receipt)
	}
	if _, err := store.Transition(t.Context(), id, operation.StageCompleted, operation.Patch{
		Delivery:      operation.DeliveryTransportAccepted,
		Verification:  operation.Verification{Status: operation.VerificationNotRequested},
		TerminalClaim: operation.TerminalClaimNotProven,
	}, now.Add(3*time.Second)); !errors.Is(err, operation.ErrInvalidTransition) {
		t.Fatalf("terminal transition error = %v, want ErrInvalidTransition", err)
	}
}

func TestConcurrentBeginExecutesOneInsert(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 9, 5, 3, 30, 0, 0, time.UTC)
	request := newTestRequest(t, uuid.New(), domain.DeviceID("stable-1"), 3, operation.EffectPower, "reset", "policy-1", []byte(`{"action":"reset"}`))

	var inserted atomic.Int64
	var existing atomic.Int64
	errorsFound := make(chan error, 16)
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			_, wasExisting, err := store.Begin(t.Context(), request, now)
			if err != nil {
				errorsFound <- err
				return
			}
			if wasExisting {
				existing.Add(1)
			} else {
				inserted.Add(1)
			}
		})
	}
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent Begin() error: %v", err)
	}
	if inserted.Load() != 1 || existing.Load() != 15 {
		t.Fatalf("inserted=%d existing=%d, want 1 and 15", inserted.Load(), existing.Load())
	}
}

func TestReceiptRoundTripAndRetention(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	id := uuid.New()
	request := newTestRequest(t, id, domain.DeviceID("stable-1"), 4, operation.EffectInput, "click", "policy-2", []byte(`{"x":10,"y":20}`))
	if _, _, err := store.Begin(t.Context(), request, now); err != nil {
		t.Fatalf("Begin() error: %v", err)
	}
	if _, err := store.Transition(t.Context(), id, operation.StageSendStarted, operation.Patch{
		Delivery:      operation.DeliveryPossiblySent,
		Verification:  operation.Verification{Status: operation.VerificationNotRequested},
		TerminalClaim: operation.TerminalClaimNotProven,
	}, now.Add(time.Second)); err != nil {
		t.Fatalf("send_started error: %v", err)
	}
	if _, err := store.Transition(t.Context(), id, operation.StageTransportAccepted, operation.Patch{
		Delivery:      operation.DeliveryTransportAccepted,
		Verification:  operation.Verification{Status: operation.VerificationNotRequested},
		TerminalClaim: operation.TerminalClaimNotProven,
	}, now.Add(2*time.Second)); err != nil {
		t.Fatalf("accepted error: %v", err)
	}
	if _, err := store.Transition(t.Context(), id, operation.StageObservationStarted, operation.Patch{
		Delivery:      operation.DeliveryTransportAccepted,
		Verification:  operation.Verification{Status: operation.VerificationPending},
		TerminalClaim: operation.TerminalClaimNotProven,
	}, now.Add(3*time.Second)); err != nil {
		t.Fatalf("observation_started error: %v", err)
	}
	if _, err := store.Transition(t.Context(), id, operation.StageStateObserved, operation.Patch{
		Delivery: operation.DeliveryTransportAccepted,
		Verification: operation.Verification{
			Status: operation.VerificationObserved, Signals: []string{"video_changed"}, ObservationID: "obs-1",
		},
		TerminalClaim: operation.TerminalClaimNotProven,
		Warnings:      []string{"transport acceptance does not prove target outcome"},
	}, now.Add(4*time.Second)); err != nil {
		t.Fatalf("state_observed error: %v", err)
	}
	receipt, err := store.Transition(t.Context(), id, operation.StageCompleted, operation.Patch{
		Delivery: operation.DeliveryTransportAccepted,
		Verification: operation.Verification{
			Status: operation.VerificationObserved, Signals: []string{"video_changed"}, ObservationID: "obs-1",
		},
		TerminalClaim: operation.TerminalClaimNotProven,
		Warnings:      []string{"transport acceptance does not prove target outcome"},
	}, now.Add(5*time.Second))
	if err != nil {
		t.Fatalf("completed error: %v", err)
	}
	if len(receipt.Verification.Signals) != 1 || len(receipt.Warnings) != 1 || receipt.Verification.ObservationID != "obs-1" {
		t.Fatalf("receipt evidence did not round-trip: %+v", receipt)
	}
	if purged, err := store.PurgeTerminalBefore(t.Context(), now.Add(5*time.Second)); err != nil || purged != 0 {
		t.Fatalf("exclusive cutoff purge = (%d, %v), want (0, nil)", purged, err)
	}
	if purged, err := store.PurgeTerminalBefore(t.Context(), now.Add(6*time.Second)); err != nil || purged != 1 {
		t.Fatalf("retention purge = (%d, %v), want (1, nil)", purged, err)
	}
	if _, err := store.Get(t.Context(), id); !errors.Is(err, operation.ErrNotFound) {
		t.Fatalf("Get() after purge error = %v, want ErrNotFound", err)
	}
}

func TestOpenRecoversInterruptedSend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("first Open() error: %v", err)
	}
	id := uuid.New()
	request := newTestRequest(t, id, domain.DeviceID("stable-1"), 9, operation.EffectInput, "key_press", "policy-3", []byte(`{"key":"ENTER"}`))
	if _, _, err := store.Begin(t.Context(), request, time.Now()); err != nil {
		t.Fatalf("Begin() error: %v", err)
	}
	if _, err := store.Transition(t.Context(), id, operation.StageSendStarted, operation.Patch{
		Delivery:      operation.DeliveryPossiblySent,
		Verification:  operation.Verification{Status: operation.VerificationNotRequested},
		TerminalClaim: operation.TerminalClaimNotProven,
	}, time.Now()); err != nil {
		t.Fatalf("send_started error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("first Close() error: %v", err)
	}

	store, err = Open(t.Context(), path)
	if err != nil {
		t.Fatalf("second Open() error: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	receipt, err := store.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if receipt.Stage != operation.StageAmbiguous || receipt.RetrySafe {
		t.Fatalf("restart recovery receipt = %+v", receipt)
	}
}

func TestMigrationIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	first, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("first Open() error: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error: %v", err)
	}
	second, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("second Open() error: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second Close() error: %v", err)
	}
}

func TestMigrationChecksumMismatchFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	if _, err := store.db.ExecContext(t.Context(),
		"UPDATE schema_migrations SET checksum = zeroblob(32) WHERE version = 1"); err != nil {
		t.Fatalf("corrupt migration checksum: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	if reopened, err := Open(t.Context(), path); err == nil {
		reopened.Close()
		t.Fatal("Open() succeeded with a mismatched migration checksum")
	}
}
