// Package confirmation owns short-lived, single-use proofs for high-risk
// device operations. Proofs are opaque to adapters and are consumed only by
// the hardware execution service at the final send boundary.
package confirmation

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"sync"
	"time"
	"uuid"

	"github.com/kaaanata/jetkvm-cli/internal/domain"
)

const (
	DefaultTTL   = 2 * time.Minute
	tokenVersion = 1
)

var (
	ErrInvalidConfiguration = errors.New("invalid confirmation configuration")
	ErrPrincipalRequired    = errors.New("authenticated principal is required")
	ErrProofRequired        = errors.New("confirmation proof is required")
	ErrProofInvalid         = errors.New("confirmation proof is invalid")
	ErrProofExpired         = errors.New("confirmation proof has expired")
	ErrProofMismatch        = errors.New("confirmation proof does not match the operation")
	ErrProofReplayed        = errors.New("confirmation proof was already consumed")
)

// Principal is the authenticated caller identity established by a trusted
// transport adapter. ID must be stable within the adapter's trust domain.
type Principal struct {
	ID        string
	Transport string
}

// LocalProcessPrincipal is the identity used by the local stdio adapter.
func LocalProcessPrincipal() Principal {
	return Principal{ID: "local-process", Transport: "stdio"}
}

// WithPrincipal attaches the identity established by stdio process ownership
// or HTTP authentication. Device-facing request arguments must never choose it.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// PrincipalFromContext returns the transport-authenticated caller identity.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok && principal.ID != "" && principal.Transport != ""
}

// Binding is the complete authority identity for one high-risk operation.
// ArgumentsDigest must cover deterministic, versioned canonical arguments.
type Binding struct {
	DeviceID        domain.DeviceID
	Generation      uint64
	Effect          domain.EffectClass
	Action          string
	ArgumentsDigest [sha256.Size]byte
	PolicyRevision  string
}

// DigestArguments derives the unkeyed identity of canonical arguments. The
// HMAC-sealed proof prevents callers from substituting this digest.
func DigestArguments(canonical []byte) [sha256.Size]byte {
	return sha256.Sum256(canonical)
}

// Proof is intentionally opaque. Only an Issuer can create a valid value.
type Proof struct{ sealed string }

// WithProof attaches a proof issued after a trusted confirmation interaction.
func WithProof(ctx context.Context, proof Proof) context.Context {
	return context.WithValue(ctx, proofContextKey{}, proof)
}

// NonceStore is the atomic single-use authority. Implementations used by
// multi-process servers must place this state in their shared durable store.
type NonceStore interface {
	Consume(context.Context, uuid.UUID, time.Time) error
}

// MemoryNonceStore is suitable for the single-process stdio and loopback HTTP
// deployment supported by the first release.
type MemoryNonceStore struct {
	mu       sync.Mutex
	consumed map[uuid.UUID]time.Time
}

func NewMemoryNonceStore() *MemoryNonceStore {
	return &MemoryNonceStore{consumed: make(map[uuid.UUID]time.Time)}
}

func (s *MemoryNonceStore) Consume(_ context.Context, nonce uuid.UUID, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for existing, expiry := range s.consumed {
		if !expiry.After(now) {
			delete(s.consumed, existing)
		}
	}
	if _, exists := s.consumed[nonce]; exists {
		return ErrProofReplayed
	}
	s.consumed[nonce] = expiresAt
	return nil
}

type Config struct {
	Key    []byte
	TTL    time.Duration
	Nonces NonceStore
	Now    func() time.Time
}

// Authority mints and verifies proofs. Key material is supplied by the
// composition root and is never generated or persisted by this package.
type Authority struct {
	key    []byte
	ttl    time.Duration
	nonces NonceStore
	now    func() time.Time
}

func NewAuthority(config Config) (*Authority, error) {
	if len(config.Key) < sha256.Size {
		return nil, fmt.Errorf("%w: key must contain at least 32 bytes", ErrInvalidConfiguration)
	}
	if config.TTL == 0 {
		config.TTL = DefaultTTL
	}
	if config.TTL <= 0 || config.Nonces == nil {
		return nil, fmt.Errorf("%w: positive TTL and nonce store are required", ErrInvalidConfiguration)
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Authority{key: bytes.Clone(config.Key), ttl: config.TTL, nonces: config.Nonces, now: config.Now}, nil
}

// Mint creates a short-lived proof for the principal already authenticated in
// ctx. Minting is reserved for trusted adapters after user confirmation.
func (a *Authority) Mint(ctx context.Context, binding Binding) (Proof, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return Proof{}, ErrPrincipalRequired
	}
	if err := validateBinding(binding); err != nil {
		return Proof{}, err
	}
	now := a.now()
	payload := tokenPayload{
		Version: tokenVersion, Nonce: uuid.NewV7(), PrincipalID: principal.ID,
		Transport: principal.Transport, DeviceID: binding.DeviceID,
		Generation: binding.Generation, Effect: binding.Effect, Action: binding.Action,
		ArgumentsDigest: hex.EncodeToString(binding.ArgumentsDigest[:]),
		PolicyRevision:  binding.PolicyRevision, IssuedAt: now, ExpiresAt: now.Add(a.ttl),
	}
	encoded, err := json.Marshal(payload, json.Deterministic(true))
	if err != nil {
		return Proof{}, fmt.Errorf("encode confirmation proof: %w", err)
	}
	mac := hmac.New(sha256.New, a.key)
	mac.Write(encoded)
	sealed := base64.RawURLEncoding.EncodeToString(encoded) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return Proof{sealed: sealed}, nil
}

// VerifyAndConsume validates every bound field and atomically consumes the
// nonce. Callers must invoke it immediately before crossing the send boundary.
func (a *Authority) VerifyAndConsume(ctx context.Context, expected Binding) error {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return ErrPrincipalRequired
	}
	if err := validateBinding(expected); err != nil {
		return err
	}
	proof, ok := ctx.Value(proofContextKey{}).(Proof)
	if !ok || proof.sealed == "" {
		return ErrProofRequired
	}
	payload, err := a.open(proof)
	if err != nil {
		return err
	}
	now := a.now()
	if payload.ExpiresAt.Before(now) || payload.ExpiresAt.Equal(now) || payload.IssuedAt.After(now) {
		return ErrProofExpired
	}
	if payload.PrincipalID != principal.ID || payload.Transport != principal.Transport ||
		payload.DeviceID != expected.DeviceID || payload.Generation != expected.Generation ||
		payload.Effect != expected.Effect || payload.Action != expected.Action ||
		payload.ArgumentsDigest != hex.EncodeToString(expected.ArgumentsDigest[:]) ||
		payload.PolicyRevision != expected.PolicyRevision {
		return ErrProofMismatch
	}
	if err := a.nonces.Consume(ctx, payload.Nonce, payload.ExpiresAt); err != nil {
		return err
	}
	return nil
}

func (a *Authority) open(proof Proof) (tokenPayload, error) {
	encodedPart, macPart, ok := splitToken(proof.sealed)
	if !ok {
		return tokenPayload{}, ErrProofInvalid
	}
	encoded, err := base64.RawURLEncoding.DecodeString(encodedPart)
	if err != nil {
		return tokenPayload{}, ErrProofInvalid
	}
	providedMAC, err := base64.RawURLEncoding.DecodeString(macPart)
	if err != nil {
		return tokenPayload{}, ErrProofInvalid
	}
	mac := hmac.New(sha256.New, a.key)
	mac.Write(encoded)
	if !hmac.Equal(providedMAC, mac.Sum(nil)) {
		return tokenPayload{}, ErrProofInvalid
	}
	var payload tokenPayload
	if err := json.Unmarshal(encoded, &payload, json.RejectUnknownMembers(true)); err != nil {
		return tokenPayload{}, ErrProofInvalid
	}
	if payload.Version != tokenVersion || payload.Nonce == uuid.Nil() {
		return tokenPayload{}, ErrProofInvalid
	}
	return payload, nil
}

func validateBinding(binding Binding) error {
	if binding.DeviceID == "" || binding.Action == "" || binding.PolicyRevision == "" || binding.ArgumentsDigest == [sha256.Size]byte{} {
		return fmt.Errorf("%w: incomplete binding", ErrProofMismatch)
	}
	switch binding.Effect {
	case domain.EffectObserve, domain.EffectInput, domain.EffectPower, domain.EffectMedia, domain.EffectAdmin, domain.EffectIrreversible:
		return nil
	default:
		return fmt.Errorf("%w: unknown effect %q", ErrProofMismatch, binding.Effect)
	}
}

func splitToken(token string) (string, string, bool) {
	for index := range len(token) {
		if token[index] == '.' {
			return token[:index], token[index+1:], index > 0 && index+1 < len(token)
		}
	}
	return "", "", false
}

type tokenPayload struct {
	Version         int                `json:"version"`
	Nonce           uuid.UUID          `json:"nonce"`
	PrincipalID     string             `json:"principal_id"`
	Transport       string             `json:"transport"`
	DeviceID        domain.DeviceID    `json:"device_id"`
	Generation      uint64             `json:"generation"`
	Effect          domain.EffectClass `json:"effect"`
	Action          string             `json:"action"`
	ArgumentsDigest string             `json:"arguments_digest"`
	PolicyRevision  string             `json:"policy_revision"`
	IssuedAt        time.Time          `json:"issued_at"`
	ExpiresAt       time.Time          `json:"expires_at"`
}

type principalContextKey struct{}
type proofContextKey struct{}
