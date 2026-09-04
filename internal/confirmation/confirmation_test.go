package confirmation

import (
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaaanata/jetkvm-cli/internal/domain"
)

func TestProofIsBoundAndSingleUse(t *testing.T) {
	authority := testAuthority(t, time.Now)
	ctx := WithPrincipal(t.Context(), Principal{ID: "user-1", Transport: "http"})
	binding := testBinding()
	proof, err := authority.Mint(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	confirmed := WithProof(ctx, proof)
	if err := authority.VerifyAndConsume(confirmed, binding); err != nil {
		t.Fatalf("VerifyAndConsume() error = %v", err)
	}
	if err := authority.VerifyAndConsume(confirmed, binding); !errors.Is(err, ErrProofReplayed) {
		t.Fatalf("replayed VerifyAndConsume() error = %v", err)
	}
}

func TestProofRejectsTampering(t *testing.T) {
	authority := testAuthority(t, time.Now)
	ctx := WithPrincipal(t.Context(), Principal{ID: "user-1", Transport: "http"})
	proof, err := authority.Mint(ctx, testBinding())
	if err != nil {
		t.Fatal(err)
	}
	payload, signature, ok := strings.Cut(proof.sealed, ".")
	if !ok || payload == "" || signature == "" {
		t.Fatal("proof did not contain sealed payload and signature")
	}
	if payload[0] == 'A' {
		payload = "B" + payload[1:]
	} else {
		payload = "A" + payload[1:]
	}
	proof.sealed = payload + "." + signature
	if err := authority.VerifyAndConsume(WithProof(ctx, proof), testBinding()); !errors.Is(err, ErrProofInvalid) {
		t.Fatalf("tampered VerifyAndConsume() error = %v", err)
	}
}

func TestProofRejectsExpired(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	authority := testAuthority(t, clock)
	ctx := WithPrincipal(t.Context(), Principal{ID: "local-process", Transport: "stdio"})
	proof, err := authority.Mint(ctx, testBinding())
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(DefaultTTL)
	if err := authority.VerifyAndConsume(WithProof(ctx, proof), testBinding()); !errors.Is(err, ErrProofExpired) {
		t.Fatalf("expired VerifyAndConsume() error = %v", err)
	}
}

func TestProofRejectsWrongTargetAndPrincipal(t *testing.T) {
	authority := testAuthority(t, time.Now)
	issuerContext := WithPrincipal(t.Context(), Principal{ID: "user-1", Transport: "http"})
	proof, err := authority.Mint(issuerContext, testBinding())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		principal Principal
		mutate    func(*Binding)
	}{
		{name: "principal", principal: Principal{ID: "user-2", Transport: "http"}},
		{name: "device", principal: Principal{ID: "user-1", Transport: "http"}, mutate: func(binding *Binding) { binding.DeviceID = "device-2" }},
		{name: "generation", principal: Principal{ID: "user-1", Transport: "http"}, mutate: func(binding *Binding) { binding.Generation++ }},
		{name: "action", principal: Principal{ID: "user-1", Transport: "http"}, mutate: func(binding *Binding) { binding.Action = "power.hold" }},
		{name: "arguments", principal: Principal{ID: "user-1", Transport: "http"}, mutate: func(binding *Binding) { binding.ArgumentsDigest = sha256.Sum256([]byte("other")) }},
		{name: "policy", principal: Principal{ID: "user-1", Transport: "http"}, mutate: func(binding *Binding) { binding.PolicyRevision = "sha256:other" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binding := testBinding()
			if test.mutate != nil {
				test.mutate(&binding)
			}
			ctx := WithPrincipal(t.Context(), test.principal)
			err := authority.VerifyAndConsume(WithProof(ctx, proof), binding)
			if !errors.Is(err, ErrProofMismatch) {
				t.Fatalf("VerifyAndConsume() error = %v", err)
			}
		})
	}
}

func testAuthority(t *testing.T, now func() time.Time) *Authority {
	t.Helper()
	authority, err := NewAuthority(Config{
		Key: []byte("0123456789abcdef0123456789abcdef"), Nonces: NewMemoryNonceStore(), Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func testBinding() Binding {
	return Binding{
		DeviceID: "device-1", Generation: 7, Effect: domain.EffectPower,
		Action: "power.reset", ArgumentsDigest: DigestArguments([]byte("canonical")),
		PolicyRevision: "sha256:policy",
	}
}
