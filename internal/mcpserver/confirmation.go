package mcpserver

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"uuid"

	coreconfirmation "github.com/kaaanata/jetkvm-cli/internal/confirmation"
	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
)

var (
	ErrConfirmationInvalid  = errors.New("confirmation state is invalid")
	ErrConfirmationExpired  = errors.New("confirmation state has expired")
	ErrConfirmationReplayed = errors.New("confirmation state was already consumed")
)

// ConfirmationRequest is the complete authority-bound identity of one
// confirmation round. ArgumentsDigest must cover the canonical tool input.
type ConfirmationRequest struct {
	Principal       string
	DeviceID        domain.DeviceID
	ControlHandle   control.HandleID
	Generation      uint64
	OperationKind   string
	ArgumentsDigest string
	ObservationID   string
	PolicyRevision  string
	Binding         coreconfirmation.Binding
}

// ConfirmationIssuer seals request state and atomically consumes its nonce.
// Confirm returns a context carrying the verified proof for downstream
// authorizers; callers must use that returned context for execution.
type ConfirmationIssuer interface {
	Issue(context.Context, ConfirmationRequest) (string, error)
	Confirm(context.Context, string, ConfirmationRequest) (context.Context, error)
}

type sealedConfirmationIssuer struct {
	key       []byte
	ttl       time.Duration
	now       func() time.Time
	mu        sync.Mutex
	issued    map[uuid.UUID]time.Time
	consumed  map[uuid.UUID]struct{}
	authority *coreconfirmation.Authority
}

type sealedConfirmationState struct {
	ConfirmationRequest
	Nonce     uuid.UUID `json:"nonce"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// NewConfirmationIssuer constructs the process authority used by stdio and
// the first-release single-instance loopback HTTP server. Restarting the
// process invalidates outstanding states and therefore fails closed.
func NewConfirmationIssuer(key []byte, ttl time.Duration, terminal ...*coreconfirmation.Authority) (ConfirmationIssuer, error) {
	if len(key) < sha256.Size {
		return nil, errors.New("confirmation key must contain at least 32 bytes")
	}
	if ttl <= 0 {
		ttl = defaultConfirmationTTL
	}
	var authority *coreconfirmation.Authority
	if len(terminal) > 0 {
		authority = terminal[0]
	}
	if authority == nil {
		var err error
		authority, err = coreconfirmation.NewAuthority(coreconfirmation.Config{Key: key, Nonces: coreconfirmation.NewMemoryNonceStore()})
		if err != nil {
			return nil, err
		}
	}
	return &sealedConfirmationIssuer{
		key:       bytes.Clone(key),
		ttl:       ttl,
		now:       time.Now,
		issued:    make(map[uuid.UUID]time.Time),
		consumed:  make(map[uuid.UUID]struct{}),
		authority: authority,
	}, nil
}

func (i *sealedConfirmationIssuer) Issue(_ context.Context, request ConfirmationRequest) (string, error) {
	if err := request.validate(); err != nil {
		return "", err
	}
	now := i.now().UTC()
	state := sealedConfirmationState{
		ConfirmationRequest: request,
		Nonce:               uuid.NewV7(),
		IssuedAt:            now,
		ExpiresAt:           now.Add(i.ttl),
	}
	payload, err := json.Marshal(state, json.Deterministic(true))
	if err != nil {
		return "", fmt.Errorf("seal confirmation state: %w", err)
	}
	mac := hmac.New(sha256.New, i.key)
	_, _ = mac.Write(payload)

	i.mu.Lock()
	i.pruneLocked(now)
	i.issued[state.Nonce] = state.ExpiresAt
	i.mu.Unlock()

	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (i *sealedConfirmationIssuer) Confirm(ctx context.Context, token string, expected ConfirmationRequest) (context.Context, error) {
	if err := expected.validate(); err != nil {
		return ctx, err
	}
	payloadPart, signaturePart, ok := strings.Cut(token, ".")
	if !ok || payloadPart == "" || signaturePart == "" {
		return ctx, ErrConfirmationInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil {
		return ctx, ErrConfirmationInvalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(signaturePart)
	if err != nil {
		return ctx, ErrConfirmationInvalid
	}
	mac := hmac.New(sha256.New, i.key)
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return ctx, ErrConfirmationInvalid
	}
	var state sealedConfirmationState
	if err := json.Unmarshal(payload, &state, json.RejectUnknownMembers(true)); err != nil || state.ConfirmationRequest != expected {
		return ctx, ErrConfirmationInvalid
	}
	now := i.now().UTC()
	if !now.Before(state.ExpiresAt) || state.IssuedAt.After(now) {
		return ctx, ErrConfirmationExpired
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	i.pruneLocked(now)
	if _, used := i.consumed[state.Nonce]; used {
		return ctx, ErrConfirmationReplayed
	}
	expiresAt, issued := i.issued[state.Nonce]
	if !issued || !expiresAt.Equal(state.ExpiresAt) {
		return ctx, ErrConfirmationInvalid
	}
	delete(i.issued, state.Nonce)
	i.consumed[state.Nonce] = struct{}{}
	principalCtx := coreconfirmation.WithPrincipal(ctx, coreconfirmation.Principal{ID: expected.Principal, Transport: "mcp"})
	proof, err := i.authority.Mint(principalCtx, expected.Binding)
	if err != nil {
		return ctx, err
	}
	return coreconfirmation.WithProof(principalCtx, proof), nil
}

func (i *sealedConfirmationIssuer) pruneLocked(now time.Time) {
	for nonce, expiresAt := range i.issued {
		if !now.Before(expiresAt) {
			delete(i.issued, nonce)
		}
	}
}

func (r ConfirmationRequest) validate() error {
	if r.Principal == "" || r.DeviceID == "" || r.OperationKind == "" || r.ArgumentsDigest == "" || r.PolicyRevision == "" ||
		r.Binding.DeviceID != r.DeviceID || r.Binding.Generation != r.Generation || r.Binding.Action != r.OperationKind ||
		r.Binding.PolicyRevision != r.PolicyRevision || r.Binding.ArgumentsDigest == [sha256.Size]byte{} ||
		r.ArgumentsDigest != hex.EncodeToString(r.Binding.ArgumentsDigest[:]) {
		return ErrConfirmationInvalid
	}
	return nil
}
