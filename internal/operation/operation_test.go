package operation

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/kaaanata/jetkvm-cli/internal/domain"
)

func TestNewRequestDigestBindsAllRequestIdentity(t *testing.T) {
	digester := testDigester(t)
	id := uuid.New()
	base, err := digester.NewRequest(id, domain.DeviceID("device-1"), 7, EffectInput, "type", "policy-1", []byte(`{"text":"secret"}`))
	if err != nil {
		t.Fatalf("NewRequest() error: %v", err)
	}
	same, err := digester.NewRequest(id, domain.DeviceID("device-1"), 7, EffectInput, "type", "policy-1", []byte(`{"text":"secret"}`))
	if err != nil {
		t.Fatalf("NewRequest() error: %v", err)
	}
	changed, err := digester.NewRequest(id, domain.DeviceID("device-1"), 8, EffectInput, "type", "policy-1", []byte(`{"text":"secret"}`))
	if err != nil {
		t.Fatalf("NewRequest() error: %v", err)
	}

	if base.Digest != same.Digest {
		t.Fatal("same canonical request produced different digests")
	}
	if base.Digest == changed.Digest {
		t.Fatal("generation change did not change digest")
	}
	otherDigester, err := NewDigester([]byte("abcdef0123456789abcdef0123456789"))
	if err != nil {
		t.Fatalf("NewDigester() error: %v", err)
	}
	otherKey, err := otherDigester.NewRequest(id, domain.DeviceID("device-1"), 7, EffectInput, "type", "policy-1", []byte(`{"text":"secret"}`))
	if err != nil {
		t.Fatalf("NewRequest() error: %v", err)
	}
	if base.Digest == otherKey.Digest {
		t.Fatal("different HMAC key produced the same digest")
	}
	if base.Digest == [sha256.Size]byte{} {
		t.Fatal("request digest is zero")
	}
}

func TestPurgeRetainedReceiptsUsesRetentionCutoff(t *testing.T) {
	now := time.Date(2026, 9, 5, 6, 0, 0, 0, time.UTC)
	repository := &retentionRepository{}
	service := NewServiceWithClock(repository, fixedClock{now: now})

	if _, err := service.PurgeRetainedReceipts(t.Context(), 0); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("zero retention error = %v, want ErrInvalidRequest", err)
	}
	if _, err := service.PurgeRetainedReceipts(t.Context(), DefaultReceiptRetention); err != nil {
		t.Fatalf("PurgeRetainedReceipts() error: %v", err)
	}
	want := now.Add(-30 * 24 * time.Hour)
	if !repository.cutoff.Equal(want) {
		t.Fatalf("cutoff = %v, want %v", repository.cutoff, want)
	}
}

func TestNewDigesterRequiresStrongKey(t *testing.T) {
	if _, err := NewDigester([]byte("short")); !errors.Is(err, ErrInvalidDigestKey) {
		t.Fatalf("NewDigester() error = %v, want ErrInvalidDigestKey", err)
	}
}

func TestZeroDigesterCannotCreateRequest(t *testing.T) {
	if _, err := (Digester{}).NewRequest(uuid.New(), domain.DeviceID("device-1"), 1, EffectInput, "type", "policy-1", nil); !errors.Is(err, ErrInvalidDigestKey) {
		t.Fatalf("NewRequest() error = %v, want ErrInvalidDigestKey", err)
	}
}

func TestValidatePatch(t *testing.T) {
	tests := []struct {
		name    string
		from    Stage
		to      Stage
		patch   Patch
		wantErr bool
	}{
		{
			name: "cross send boundary",
			from: StageNotSent, to: StageSendStarted,
			patch: validPatch(DeliveryPossiblySent),
		},
		{
			name: "reject replay safe send",
			from: StageNotSent, to: StageSendStarted,
			patch: func() Patch { p := validPatch(DeliveryPossiblySent); p.RetrySafe = true; return p }(), wantErr: true,
		},
		{
			name: "transport accepted",
			from: StageSendStarted, to: StageTransportAccepted,
			patch: validPatch(DeliveryTransportAccepted),
		},
		{
			name: "ambiguous",
			from: StageSendStarted, to: StageAmbiguous,
			patch: validPatch(DeliveryPossiblySent),
		},
		{
			name: "pre-send cancellation",
			from: StageNotSent, to: StageCancelled,
			patch: func() Patch { p := validPatch(DeliveryNotSent); p.RetrySafe = true; return p }(),
		},
		{
			name: "terminal cannot transition",
			from: StageAmbiguous, to: StageCompleted,
			patch: validPatch(DeliveryTransportAccepted), wantErr: true,
		},
		{
			name: "cannot skip send boundary",
			from: StageNotSent, to: StageTransportAccepted,
			patch: validPatch(DeliveryTransportAccepted), wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidatePatch(test.from, test.to, test.patch)
			if test.wantErr && !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("ValidatePatch() error = %v, want ErrInvalidTransition", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("ValidatePatch() unexpected error: %v", err)
			}
		})
	}
}

func validPatch(delivery Delivery) Patch {
	return Patch{
		Delivery:      delivery,
		Verification:  Verification{Status: VerificationNotRequested},
		TerminalClaim: TerminalClaimNotProven,
	}
}

func testDigester(t *testing.T) Digester {
	t.Helper()
	digester, err := NewDigester([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewDigester() error: %v", err)
	}
	return digester
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type retentionRepository struct {
	Repository
	cutoff time.Time
}

func (r *retentionRepository) PurgeTerminalBefore(_ context.Context, cutoff time.Time) (int64, error) {
	r.cutoff = cutoff
	return 0, nil
}
