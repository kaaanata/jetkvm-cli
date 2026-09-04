// Package operation defines the durable state machine for device-changing operations.
package operation

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"
	"uuid"

	"github.com/kaaanata/jetkvm-cli/internal/domain"
)

var (
	ErrConflict          = errors.New("operation ID is already bound to a different request")
	ErrNotFound          = errors.New("operation was not found")
	ErrInvalidDigestKey  = errors.New("operation digest key must contain at least 32 bytes")
	ErrInvalidRequest    = errors.New("invalid operation request")
	ErrInvalidTransition = errors.New("invalid operation state transition")
)

const (
	TerminalClaimNotProven  = "not_proven"
	DefaultReceiptRetention = 30 * 24 * time.Hour
)

// EffectClass aliases the domain's single effect classification authority.
type EffectClass = domain.EffectClass

const (
	EffectInput = domain.EffectInput
	EffectPower = domain.EffectPower
	EffectMedia = domain.EffectMedia
	EffectAdmin = domain.EffectAdmin
)

// Stage records how far execution progressed. SendStarted is the durable point
// after which an interrupted non-idempotent operation must never be replayed.
type Stage string

const (
	StageNotSent            Stage = "not_sent"
	StageSendStarted        Stage = "send_started"
	StageTransportAccepted  Stage = "transport_accepted"
	StageObservationStarted Stage = "observation_started"
	StageStateObserved      Stage = "state_observed"
	StageCompleted          Stage = "completed"
	StageFailed             Stage = "failed"
	StageAmbiguous          Stage = "ambiguous"
	StageCancelled          Stage = "cancelled"
)

// Delivery describes only transport delivery, not the physical outcome.
type Delivery string

const (
	DeliveryNotSent           Delivery = "not_sent"
	DeliveryPossiblySent      Delivery = "possibly_sent"
	DeliveryTransportAccepted Delivery = "transport_accepted"
)

// VerificationStatus describes independently observed state after delivery.
type VerificationStatus string

const (
	VerificationNotRequested VerificationStatus = "not_requested"
	VerificationPending      VerificationStatus = "pending"
	VerificationObserved     VerificationStatus = "observed"
	VerificationNotObserved  VerificationStatus = "not_observed"
)

// Request is the immutable identity of a state-changing operation. Digest must
// cover the canonicalized complete arguments and the other request fields.
type Request struct {
	ID                uuid.UUID
	Digest            [sha256.Size]byte
	DeviceID          domain.DeviceID
	ControlGeneration uint64
	Effect            EffectClass
	Action            string
	PolicyRevision    string
}

// Digester creates keyed request digests so low-entropy input such as typed
// text cannot be recovered from the durable ledger by offline guessing.
type Digester struct {
	key []byte
}

// NewDigester constructs a request digester from a stable application secret.
func NewDigester(key []byte) (Digester, error) {
	if len(key) < sha256.Size {
		return Digester{}, ErrInvalidDigestKey
	}
	return Digester{key: bytes.Clone(key)}, nil
}

// Validate checks the immutable request identity before it enters the ledger.
func (r Request) Validate() error {
	if r.ID == uuid.Nil() {
		return fmt.Errorf("%w: operation ID is required", ErrInvalidRequest)
	}
	if r.DeviceID == "" || r.Action == "" || r.PolicyRevision == "" {
		return fmt.Errorf("%w: device ID, action, and policy revision are required", ErrInvalidRequest)
	}
	if r.ControlGeneration > math.MaxInt64 {
		return fmt.Errorf("%w: control generation exceeds durable range", ErrInvalidRequest)
	}
	switch r.Effect {
	case EffectInput, EffectPower, EffectMedia, EffectAdmin:
	default:
		return fmt.Errorf("%w: unknown effect class %q", ErrInvalidRequest, r.Effect)
	}
	if r.Digest == [sha256.Size]byte{} {
		return fmt.Errorf("%w: request digest is required", ErrInvalidRequest)
	}
	return nil
}

// NewRequest creates a keyed request digest without retaining the canonical
// arguments. Callers must use a deterministic, versioned canonical encoding.
func (d Digester) NewRequest(id uuid.UUID, deviceID domain.DeviceID, generation uint64, effect EffectClass, action, policyRevision string, canonicalArguments []byte) (Request, error) {
	if len(d.key) < sha256.Size {
		return Request{}, ErrInvalidDigestKey
	}
	h := hmac.New(sha256.New, d.key)
	writeDigestField(h, []byte("jetkvm-operation-v1"))
	writeDigestField(h, id[:])
	writeDigestField(h, []byte(deviceID))
	var encodedGeneration [8]byte
	binary.BigEndian.PutUint64(encodedGeneration[:], generation)
	writeDigestField(h, encodedGeneration[:])
	writeDigestField(h, []byte(effect))
	writeDigestField(h, []byte(action))
	writeDigestField(h, []byte(policyRevision))
	writeDigestField(h, canonicalArguments)

	var digest [sha256.Size]byte
	copy(digest[:], h.Sum(nil))
	request := Request{
		ID:                id,
		Digest:            digest,
		DeviceID:          deviceID,
		ControlGeneration: generation,
		Effect:            effect,
		Action:            action,
		PolicyRevision:    policyRevision,
	}
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	return request, nil
}

type digestWriter interface {
	Write([]byte) (int, error)
}

func writeDigestField(writer digestWriter, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	writer.Write(size[:])
	writer.Write(value)
}

// Verification records independent evidence without claiming a physical outcome.
type Verification struct {
	Status        VerificationStatus
	Signals       []string
	ObservationID string
}

// Receipt is the durable, redacted record returned for an operation.
type Receipt struct {
	Request
	Stage         Stage
	Delivery      Delivery
	Verification  Verification
	TerminalClaim string
	RetrySafe     bool
	ErrorKind     string
	Warnings      []string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	SendStartedAt time.Time
	TerminalAt    time.Time
}

// IsTerminal reports whether no further state transition is permitted.
func (r Receipt) IsTerminal() bool {
	return r.Stage.IsTerminal()
}

// IsTerminal reports whether no further state transition is permitted.
func (s Stage) IsTerminal() bool {
	switch s {
	case StageCompleted, StageFailed, StageAmbiguous, StageCancelled:
		return true
	default:
		return false
	}
}

// ValidateTransition enforces the sole operation state-transition authority.
func ValidateTransition(from, to Stage) error {
	if from.IsTerminal() || from == to {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
	}

	allowed := false
	switch from {
	case StageNotSent:
		allowed = to == StageSendStarted || to == StageFailed || to == StageCancelled
	case StageSendStarted:
		allowed = to == StageTransportAccepted || to == StageAmbiguous
	case StageTransportAccepted:
		allowed = to == StageObservationStarted || to == StageCompleted || to == StageFailed
	case StageObservationStarted:
		allowed = to == StageStateObserved || to == StageCompleted || to == StageFailed
	case StageStateObserved:
		allowed = to == StageCompleted || to == StageFailed
	}
	if !allowed {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
	}
	return nil
}

// ValidatePatch enforces delivery and retry semantics for a destination stage.
func ValidatePatch(from, to Stage, patch Patch) error {
	if err := ValidateTransition(from, to); err != nil {
		return err
	}
	invalid := func(reason string) error {
		return fmt.Errorf("%w: %s -> %s: %s", ErrInvalidTransition, from, to, reason)
	}
	if patch.TerminalClaim == "" {
		return invalid("terminal claim is required")
	}
	switch patch.Verification.Status {
	case VerificationNotRequested, VerificationPending, VerificationObserved, VerificationNotObserved:
	default:
		return invalid("verification status is required")
	}
	switch to {
	case StageSendStarted:
		if patch.Delivery != DeliveryPossiblySent || patch.RetrySafe {
			return invalid("send_started requires possibly_sent and retry_safe=false")
		}
	case StageTransportAccepted, StageObservationStarted, StageStateObserved, StageCompleted:
		if patch.Delivery != DeliveryTransportAccepted || patch.RetrySafe {
			return invalid("post-acceptance state requires transport_accepted and retry_safe=false")
		}
	case StageAmbiguous:
		if patch.Delivery != DeliveryPossiblySent || patch.RetrySafe {
			return invalid("ambiguous requires possibly_sent and retry_safe=false")
		}
	case StageCancelled:
		if patch.Delivery != DeliveryNotSent || !patch.RetrySafe {
			return invalid("pre-send cancellation requires not_sent and retry_safe=true")
		}
	case StageFailed:
		if from == StageNotSent {
			if patch.Delivery != DeliveryNotSent || !patch.RetrySafe {
				return invalid("pre-send failure requires not_sent and retry_safe=true")
			}
		} else if patch.Delivery != DeliveryTransportAccepted || patch.RetrySafe {
			return invalid("post-acceptance failure requires transport_accepted and retry_safe=false")
		}
	}
	if to == StageObservationStarted && patch.Verification.Status != VerificationPending {
		return invalid("observation_started requires pending verification")
	}
	if to == StageStateObserved && (patch.Verification.Status != VerificationObserved || patch.Verification.ObservationID == "") {
		return invalid("state_observed requires observed verification and an observation ID")
	}
	return nil
}

// Patch contains the state owned by one transition. Zero values deliberately
// clear optional fields so callers provide the complete resulting receipt state.
type Patch struct {
	Delivery      Delivery
	Verification  Verification
	TerminalClaim string
	RetrySafe     bool
	ErrorKind     string
	Warnings      []string
}

// Repository atomically owns operation deduplication and state persistence.
type Repository interface {
	Begin(ctx context.Context, request Request, now time.Time) (Receipt, bool, error)
	Get(ctx context.Context, id uuid.UUID) (Receipt, error)
	Transition(ctx context.Context, id uuid.UUID, to Stage, patch Patch, now time.Time) (Receipt, error)
	RecoverInterrupted(ctx context.Context, now time.Time) (int64, error)
	PurgeTerminalBefore(ctx context.Context, cutoff time.Time) (int64, error)
}
