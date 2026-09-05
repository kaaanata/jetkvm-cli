package input

import (
	"errors"
	"fmt"
	"time"
)

const (
	MaxKeyHoldDuration = 12 * time.Second
	MaxActions         = 16
	MaxBatchDuration   = 15 * time.Second
	MaxWaitDuration    = 5 * time.Second
	MaxTotalWait       = 10 * time.Second
)

var (
	ErrInvalidAction    = errors.New("invalid input action")
	ErrObservationStale = errors.New("bound observation is stale")
)

type ActionType string

const (
	ActionMove        ActionType = "move"
	ActionClick       ActionType = "click"
	ActionDoubleClick ActionType = "double_click"
	ActionDrag        ActionType = "drag"
	ActionScroll      ActionType = "scroll"
	ActionKeypress    ActionType = "keypress"
	ActionKeyHold     ActionType = "key_hold"
	ActionTypeText    ActionType = "type"
	ActionWait        ActionType = "wait"
	ActionScreenshot  ActionType = "screenshot"
)

type Button string

const (
	ButtonLeft    Button = "left"
	ButtonRight   Button = "right"
	ButtonMiddle  Button = "middle"
	ButtonBack    Button = "back"
	ButtonForward Button = "forward"
)

type ButtonMask byte

const (
	buttonLeftMask    ButtonMask = 1 << 0
	buttonRightMask   ButtonMask = 1 << 1
	buttonMiddleMask  ButtonMask = 1 << 2
	buttonBackMask    ButtonMask = 1 << 3
	buttonForwardMask ButtonMask = 1 << 4
)

type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Action is a closed union. Validate rejects fields that do not belong to Type.
type Action struct {
	Type     ActionType    `json:"type"`
	X        int           `json:"x,omitzero"`
	Y        int           `json:"y,omitzero"`
	Button   Button        `json:"button,omitempty"`
	Path     []Point       `json:"path,omitempty"`
	DeltaX   int           `json:"delta_x,omitzero"`
	DeltaY   int           `json:"delta_y,omitzero"`
	Keys     []string      `json:"keys,omitempty"`
	Text     string        `json:"text,omitempty"`
	Duration time.Duration `json:"duration,omitzero"`
}

type Batch struct {
	Observation *ObservationBinding
	Actions     []Action
}

// ObservationBinding ties pixel coordinates to one fresh video frame and
// control generation. Coordinate actions are rejected without this binding.
type ObservationBinding struct {
	ID         string
	Generation uint64
	Width      int
	Height     int
	CapturedAt time.Time
}

type compiledAction struct {
	typeName           ActionType
	point              Point
	button             ButtonMask
	path               []Point
	deltaX             int8
	deltaY             int8
	keys               []byte
	modifier           byte
	text               []Keystroke
	duration           time.Duration
	observationExpires time.Time
}

type compiledBatch struct {
	actions []compiledAction
}

type Limits struct {
	KeyHold           time.Duration
	InterKey          time.Duration
	DoubleClickDelay  time.Duration
	MaxActions        int
	MaxBatchDuration  time.Duration
	MaxWaitDuration   time.Duration
	MaxTotalWait      time.Duration
	MaxObservationAge time.Duration
}

func DefaultLimits() Limits {
	return Limits{
		KeyHold:           20 * time.Millisecond,
		InterKey:          20 * time.Millisecond,
		DoubleClickDelay:  100 * time.Millisecond,
		MaxActions:        MaxActions,
		MaxBatchDuration:  MaxBatchDuration,
		MaxWaitDuration:   MaxWaitDuration,
		MaxTotalWait:      MaxTotalWait,
		MaxObservationAge: 30 * time.Second,
	}
}

func compileBatch(batch Batch, limits Limits, generation uint64, now time.Time) (compiledBatch, error) {
	if len(batch.Actions) == 0 || len(batch.Actions) > limits.MaxActions {
		return compiledBatch{}, fmt.Errorf("%w: action count must be within 1..%d", ErrInvalidAction, limits.MaxActions)
	}
	compiled := compiledBatch{actions: make([]compiledAction, 0, len(batch.Actions))}
	var totalWait, estimated time.Duration
	for index, action := range batch.Actions {
		if action.Type == ActionScreenshot && (len(batch.Actions) != 1 && index != len(batch.Actions)-1) {
			return compiledBatch{}, fmt.Errorf("%w: screenshot must be the only or final action", ErrInvalidAction)
		}
		item, cost, err := compileAction(action, limits, batch.Observation, generation, now)
		if err != nil {
			return compiledBatch{}, fmt.Errorf("action %d: %w", index, err)
		}
		if action.Type == ActionWait {
			totalWait += action.Duration
			if totalWait > limits.MaxTotalWait {
				return compiledBatch{}, fmt.Errorf("%w: total wait exceeds %s", ErrInvalidAction, limits.MaxTotalWait)
			}
		}
		estimated += cost
		if estimated > limits.MaxBatchDuration {
			return compiledBatch{}, fmt.Errorf("%w: estimated active duration exceeds %s", ErrInvalidAction, limits.MaxBatchDuration)
		}
		compiled.actions = append(compiled.actions, item)
	}
	return compiled, nil
}

func compileAction(action Action, limits Limits, binding *ObservationBinding, generation uint64, now time.Time) (compiledAction, time.Duration, error) {
	if err := validateFields(action); err != nil {
		return compiledAction{}, 0, err
	}
	item := compiledAction{typeName: action.Type, point: Point{X: action.X, Y: action.Y}}
	switch action.Type {
	case ActionMove:
		point, err := bindPoint(item.point, binding, generation, now, limits.MaxObservationAge)
		if err != nil {
			return compiledAction{}, 0, err
		}
		item.point = point
		item.observationExpires = binding.CapturedAt.Add(limits.MaxObservationAge)
	case ActionClick, ActionDoubleClick:
		point, err := bindPoint(item.point, binding, generation, now, limits.MaxObservationAge)
		if err != nil {
			return compiledAction{}, 0, err
		}
		item.point = point
		item.observationExpires = binding.CapturedAt.Add(limits.MaxObservationAge)
		button, err := buttonMask(action.Button)
		if err != nil {
			return compiledAction{}, 0, err
		}
		item.button = button
		item.observationExpires = binding.CapturedAt.Add(limits.MaxObservationAge)
		if action.Type == ActionDoubleClick {
			return item, limits.DoubleClickDelay, nil
		}
	case ActionDrag:
		if len(action.Path) < 2 || len(action.Path) > 64 {
			return compiledAction{}, 0, fmt.Errorf("%w: drag path must contain 2..64 points", ErrInvalidAction)
		}
		item.path = make([]Point, 0, len(action.Path))
		for _, point := range action.Path {
			bound, err := bindPoint(point, binding, generation, now, limits.MaxObservationAge)
			if err != nil {
				return compiledAction{}, 0, err
			}
			item.path = append(item.path, bound)
		}
		button, err := buttonMask(action.Button)
		if err != nil {
			return compiledAction{}, 0, err
		}
		item.button = button
	case ActionScroll:
		if action.DeltaX < -127 || action.DeltaX > 127 || action.DeltaY < -127 || action.DeltaY > 127 || action.DeltaX == 0 && action.DeltaY == 0 {
			return compiledAction{}, 0, fmt.Errorf("%w: scroll deltas must be non-zero and within -127..127", ErrInvalidAction)
		}
		item.deltaX = int8(action.DeltaX)
		item.deltaY = int8(action.DeltaY)
	case ActionKeypress, ActionKeyHold:
		modifier, keys, err := CompileKeyCombo(action.Keys)
		if err != nil {
			return compiledAction{}, 0, err
		}
		item.modifier, item.keys = modifier, keys
		if action.Type == ActionKeyHold {
			if action.Duration <= 0 || action.Duration > MaxKeyHoldDuration {
				return compiledAction{}, 0, fmt.Errorf("%w: key hold must be within 1ns..%s", ErrInvalidAction, MaxKeyHoldDuration)
			}
			item.duration = action.Duration
			return item, action.Duration + limits.InterKey, nil
		}
		return item, limits.KeyHold + limits.InterKey, nil
	case ActionTypeText:
		strokes, err := CompileText(action.Text)
		if err != nil {
			return compiledAction{}, 0, err
		}
		item.text = strokes
		return item, time.Duration(len(strokes)) * (limits.KeyHold + limits.InterKey), nil
	case ActionWait:
		if action.Duration <= 0 || action.Duration > limits.MaxWaitDuration {
			return compiledAction{}, 0, fmt.Errorf("%w: wait duration must be within 1ns..%s", ErrInvalidAction, limits.MaxWaitDuration)
		}
		item.duration = action.Duration
		return item, action.Duration, nil
	case ActionScreenshot:
	default:
		return compiledAction{}, 0, fmt.Errorf("%w: unknown action type %q", ErrInvalidAction, action.Type)
	}
	return item, 0, nil
}

func validateFields(action Action) error {
	point := action.X != 0 || action.Y != 0
	hasButton := action.Button != ""
	hasPath := len(action.Path) != 0
	hasDelta := action.DeltaX != 0 || action.DeltaY != 0
	hasKeys := len(action.Keys) != 0
	hasText := action.Text != ""
	hasDuration := action.Duration != 0
	invalid := false
	switch action.Type {
	case ActionMove:
		invalid = hasButton || hasPath || hasDelta || hasKeys || hasText || hasDuration
	case ActionClick, ActionDoubleClick:
		invalid = hasPath || hasDelta || hasKeys || hasText || hasDuration
	case ActionDrag:
		invalid = point || hasDelta || hasKeys || hasText || hasDuration
	case ActionScroll:
		invalid = point || hasButton || hasPath || hasKeys || hasText || hasDuration
	case ActionKeyHold:
		invalid = point || hasButton || hasPath || hasDelta || hasText || !hasKeys || !hasDuration
	case ActionKeypress:
		invalid = point || hasButton || hasPath || hasDelta || hasText || hasDuration
	case ActionTypeText:
		invalid = point || hasButton || hasPath || hasDelta || hasKeys || hasDuration
	case ActionWait:
		invalid = point || hasButton || hasPath || hasDelta || hasKeys || hasText
	case ActionScreenshot:
		invalid = point || hasButton || hasPath || hasDelta || hasKeys || hasText || hasDuration
	default:
		return fmt.Errorf("%w: unknown action type %q", ErrInvalidAction, action.Type)
	}
	if invalid {
		return fmt.Errorf("%w: action %q contains fields from another action type", ErrInvalidAction, action.Type)
	}
	return nil
}

func bindPoint(point Point, binding *ObservationBinding, generation uint64, now time.Time, maxAge time.Duration) (Point, error) {
	if binding == nil || binding.ID == "" || binding.Width <= 0 || binding.Height <= 0 || binding.CapturedAt.IsZero() {
		return Point{}, fmt.Errorf("%w: coordinate action requires an observation binding", ErrInvalidAction)
	}
	if binding.Generation != generation {
		return Point{}, ErrStaleGeneration
	}
	age := now.Sub(binding.CapturedAt)
	if age < 0 || age > maxAge {
		return Point{}, fmt.Errorf("%w: %w", ErrInvalidAction, ErrObservationStale)
	}
	if point.X < 0 || point.X >= binding.Width || point.Y < 0 || point.Y >= binding.Height {
		return Point{}, fmt.Errorf("%w: coordinates are outside the bound observation", ErrInvalidAction)
	}
	return Point{X: scaleCoordinate(point.X, binding.Width), Y: scaleCoordinate(point.Y, binding.Height)}, nil
}

func scaleCoordinate(value, extent int) int {
	if extent == 1 {
		return 0
	}
	return int((int64(value)*absoluteCoordinate + int64(extent-1)/2) / int64(extent-1))
}

func buttonMask(button Button) (ButtonMask, error) {
	switch button {
	case "", ButtonLeft:
		return buttonLeftMask, nil
	case ButtonRight:
		return buttonRightMask, nil
	case ButtonMiddle:
		return buttonMiddleMask, nil
	case ButtonBack:
		return buttonBackMask, nil
	case ButtonForward:
		return buttonForwardMask, nil
	default:
		return 0, fmt.Errorf("%w: unknown pointer button %q", ErrInvalidAction, button)
	}
}
