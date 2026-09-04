package operation

import (
	"context"
	"fmt"
	"time"
	"uuid"
)

// Clock supplies deterministic timestamps to the operation service.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Service is the application-facing operation ledger API.
type Service struct {
	repository Repository
	clock      Clock
}

// NewService creates an operation service using the system clock.
func NewService(repository Repository) *Service {
	return NewServiceWithClock(repository, systemClock{})
}

// NewServiceWithClock creates an operation service with an injectable clock.
func NewServiceWithClock(repository Repository, clock Clock) *Service {
	return &Service{repository: repository, clock: clock}
}

// Begin atomically registers a request. Existing is true only when the exact
// same operation ID and digest were already registered.
func (s *Service) Begin(ctx context.Context, request Request) (receipt Receipt, existing bool, err error) {
	return s.repository.Begin(ctx, request, s.clock.Now())
}

// Get returns the current durable receipt.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Receipt, error) {
	return s.repository.Get(ctx, id)
}

// MarkSendStarted crosses the no-replay boundary before any device bytes are sent.
func (s *Service) MarkSendStarted(ctx context.Context, id uuid.UUID) (Receipt, error) {
	return s.repository.Transition(ctx, id, StageSendStarted, Patch{
		Delivery:      DeliveryPossiblySent,
		Verification:  Verification{Status: VerificationNotRequested},
		TerminalClaim: TerminalClaimNotProven,
		RetrySafe:     false,
	}, s.clock.Now())
}

// Transition persists one legal state transition.
func (s *Service) Transition(ctx context.Context, id uuid.UUID, to Stage, patch Patch) (Receipt, error) {
	return s.repository.Transition(ctx, id, to, patch, s.clock.Now())
}

// RecoverInterrupted converts every interrupted send into a terminal ambiguous receipt.
func (s *Service) RecoverInterrupted(ctx context.Context) (int64, error) {
	return s.repository.RecoverInterrupted(ctx, s.clock.Now())
}

// PurgeRetainedReceipts removes terminal receipts older than retention.
func (s *Service) PurgeRetainedReceipts(ctx context.Context, retention time.Duration) (int64, error) {
	if retention <= 0 {
		return 0, fmt.Errorf("%w: receipt retention must be positive", ErrInvalidRequest)
	}
	return s.repository.PurgeTerminalBefore(ctx, s.clock.Now().Add(-retention))
}
