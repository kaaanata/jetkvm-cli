package input

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type ActionStatus string

const (
	ActionNotStarted  ActionStatus = "not_started"
	ActionSendStarted ActionStatus = "send_started"
	ActionAccepted    ActionStatus = "accepted"
	ActionFailed      ActionStatus = "failed"
	ActionAmbiguous   ActionStatus = "ambiguous"
)

type BatchStatus string

const (
	BatchAccepted  BatchStatus = "accepted"
	BatchPartial   BatchStatus = "partial"
	BatchFailed    BatchStatus = "failed"
	BatchAmbiguous BatchStatus = "ambiguous"
)

type ActionReceipt struct {
	Index  int          `json:"index"`
	Type   ActionType   `json:"type"`
	Status ActionStatus `json:"status"`
	Error  string       `json:"error,omitzero"`
}

type BatchReceipt struct {
	Generation     uint64          `json:"generation"`
	Status         BatchStatus     `json:"status"`
	Actions        []ActionReceipt `json:"actions"`
	Observation    Observation     `json:"observation,omitzero"`
	Neutralized    bool            `json:"neutralized"`
	CleanupFailure string          `json:"cleanup_failure,omitzero"`
}

func (l *Lease) RunActions(ctx context.Context, batch Batch) (receipt BatchReceipt, err error) {
	if err := l.ensureValid(); err != nil {
		return BatchReceipt{}, err
	}
	compiled, err := compileBatch(batch, l.manager.limits, l.token.generation, l.manager.now())
	if err != nil {
		l.abandon()
		return BatchReceipt{}, err
	}
	receipt = BatchReceipt{
		Generation: l.token.generation,
		Status:     BatchAccepted,
		Actions:    make([]ActionReceipt, len(compiled.actions)),
	}
	for index, action := range compiled.actions {
		receipt.Actions[index] = ActionReceipt{Index: index, Type: action.typeName, Status: ActionNotStarted}
	}

	defer func() {
		cleanupErr := l.Release()
		if cleanupErr == nil {
			receipt.Neutralized = true
		} else {
			receipt.CleanupFailure = ErrNeutralization.Error()
			receipt.Status = BatchAmbiguous
			err = errors.Join(err, cleanupErr)
		}
		if recovered := recover(); recovered != nil {
			panic(recovered)
		}
	}()

	timeoutCtx, timeoutCancel := context.WithTimeoutCause(ctx, l.manager.limits.MaxBatchDuration, context.DeadlineExceeded)
	defer timeoutCancel()
	runCtx, runCancel := context.WithCancelCause(timeoutCtx)
	defer runCancel(nil)
	if err := l.manager.setActiveCancel(l.token, runCancel); err != nil {
		return receipt, err
	}
	for index, action := range compiled.actions {
		status, observation, actionErr := l.execute(runCtx, action)
		receipt.Actions[index].Status = status
		if observation.ID != "" {
			receipt.Observation = observation
		}
		if actionErr == nil {
			continue
		}
		receipt.Actions[index].Error = classifyActionError(actionErr)
		for remaining := index + 1; remaining < len(receipt.Actions); remaining++ {
			receipt.Actions[remaining].Status = ActionNotStarted
		}
		if status == ActionAmbiguous {
			receipt.Status = BatchAmbiguous
		} else if index == 0 {
			receipt.Status = BatchFailed
		} else {
			receipt.Status = BatchPartial
		}
		return receipt, actionErr
	}
	return receipt, nil
}

func (l *Lease) execute(ctx context.Context, action compiledAction) (ActionStatus, Observation, error) {
	if err := l.ensureValid(); err != nil {
		return ActionFailed, Observation{}, err
	}
	if !action.observationExpires.IsZero() && l.manager.now().After(action.observationExpires) {
		return ActionFailed, Observation{}, ErrObservationStale
	}
	switch action.typeName {
	case ActionMove:
		return l.sendPointer(ctx, action.point, 0, Motion)
	case ActionClick:
		return l.click(ctx, action.point, action.button)
	case ActionDoubleClick:
		if status, _, err := l.click(ctx, action.point, action.button); err != nil {
			return status, Observation{}, err
		}
		if err := waitContext(ctx, l.manager.limits.DoubleClickDelay); err != nil {
			return ActionAmbiguous, Observation{}, err
		}
		return l.click(ctx, action.point, action.button)
	case ActionDrag:
		return l.drag(ctx, action.path, action.button)
	case ActionScroll:
		if err := l.ensureValid(); err != nil {
			return ActionFailed, Observation{}, err
		}
		if err := l.manager.transport.SendWheel(ctx, l.token.generation, action.deltaY, action.deltaX); err != nil {
			return ActionAmbiguous, Observation{}, err
		}
		return ActionAccepted, Observation{}, nil
	case ActionKeypress:
		return l.keypress(ctx, action.modifier, action.keys)
	case ActionTypeText:
		for _, stroke := range action.text {
			status, _, err := l.keypress(ctx, stroke.Modifier, []byte{stroke.Key})
			if err != nil {
				return status, Observation{}, err
			}
		}
		return ActionAccepted, Observation{}, nil
	case ActionWait:
		if err := waitContext(ctx, action.duration); err != nil {
			return ActionFailed, Observation{}, err
		}
		return ActionAccepted, Observation{}, nil
	case ActionScreenshot:
		if l.manager.observer == nil {
			return ActionFailed, Observation{}, ErrObservationMissing
		}
		observation, err := l.manager.observer.Capture(ctx, l.token.generation)
		if err != nil {
			return ActionFailed, Observation{}, err
		}
		if observation.ID == "" || observation.Generation != l.token.generation {
			return ActionFailed, Observation{}, ErrStaleGeneration
		}
		return ActionAccepted, observation, nil
	default:
		return ActionFailed, Observation{}, ErrInvalidAction
	}
}

func (l *Lease) keypress(ctx context.Context, modifier byte, keys []byte) (ActionStatus, Observation, error) {
	pressed, err := KeyboardReport(modifier, keys...)
	if err != nil {
		return ActionFailed, Observation{}, err
	}
	released, _ := KeyboardReport(0)
	if err := l.sendHID(ctx, Reliable, pressed); err != nil {
		return ActionAmbiguous, Observation{}, err
	}
	if err := waitContext(ctx, l.manager.limits.KeyHold); err != nil {
		return ActionAmbiguous, Observation{}, err
	}
	if err := l.sendHID(ctx, Reliable, released); err != nil {
		return ActionAmbiguous, Observation{}, err
	}
	if err := waitContext(ctx, l.manager.limits.InterKey); err != nil {
		return ActionAmbiguous, Observation{}, err
	}
	return ActionAccepted, Observation{}, nil
}

func (l *Lease) click(ctx context.Context, point Point, button ButtonMask) (ActionStatus, Observation, error) {
	if status, _, err := l.sendPointer(ctx, point, 0, Reliable); err != nil {
		return status, Observation{}, err
	}
	if status, _, err := l.sendPointer(ctx, point, button, Reliable); err != nil {
		return status, Observation{}, err
	}
	return l.sendPointer(ctx, point, 0, Reliable)
}

func (l *Lease) drag(ctx context.Context, path []Point, button ButtonMask) (ActionStatus, Observation, error) {
	if status, _, err := l.sendPointer(ctx, path[0], 0, Reliable); err != nil {
		return status, Observation{}, err
	}
	for _, point := range path {
		if status, _, err := l.sendPointer(ctx, point, button, Reliable); err != nil {
			return status, Observation{}, err
		}
	}
	return l.sendPointer(ctx, path[len(path)-1], 0, Reliable)
}

func (l *Lease) sendPointer(ctx context.Context, point Point, buttons ButtonMask, reliability Reliability) (ActionStatus, Observation, error) {
	frame, err := PointerReport(point.X, point.Y, buttons)
	if err != nil {
		return ActionFailed, Observation{}, err
	}
	if err := l.sendHID(ctx, reliability, frame); err != nil {
		return ActionAmbiguous, Observation{}, err
	}
	l.manager.rememberPointer(point)
	return ActionAccepted, Observation{}, nil
}

func (l *Lease) sendHID(ctx context.Context, reliability Reliability, frame []byte) error {
	if err := l.ensureValid(); err != nil {
		return err
	}
	return l.manager.transport.SendHID(ctx, l.token.generation, reliability, frame)
}

func waitContext(ctx context.Context, duration time.Duration) error {
	if duration == 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}

func classifyActionError(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, ErrStaleGeneration):
		return "control_generation_mismatch"
	case errors.Is(err, ErrObservationMissing):
		return "capability_unavailable"
	case errors.Is(err, ErrObservationStale):
		return "observation_stale"
	default:
		return fmt.Sprintf("input_error: %T", err)
	}
}
