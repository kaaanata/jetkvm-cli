package input

import (
	"errors"
	"testing"
	"time"
)

func TestCoordinateActionsRequireFreshBoundObservation(t *testing.T) {
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	limits := DefaultLimits()
	valid := Batch{
		Observation: &ObservationBinding{ID: "obs", Generation: 7, Width: 1920, Height: 1080, CapturedAt: now},
		Actions:     []Action{{Type: ActionMove, X: 1919, Y: 1079}},
	}
	compiled, err := compileBatch(valid, limits, 7, now)
	if err != nil {
		t.Fatal(err)
	}
	if point := compiled.actions[0].point; point != (Point{X: 32767, Y: 32767}) {
		t.Fatalf("mapped point = %+v", point)
	}

	for name, mutate := range map[string]func(*Batch){
		"missing":    func(batch *Batch) { batch.Observation = nil },
		"generation": func(batch *Batch) { batch.Observation.Generation = 6 },
		"stale":      func(batch *Batch) { batch.Observation.CapturedAt = now.Add(-limits.MaxObservationAge - time.Second) },
		"bounds":     func(batch *Batch) { batch.Actions[0].X = 1920 },
	} {
		t.Run(name, func(t *testing.T) {
			batch := valid
			observation := *valid.Observation
			batch.Observation = &observation
			batch.Actions = append([]Action(nil), valid.Actions...)
			mutate(&batch)
			if _, err := compileBatch(batch, limits, 7, now); err == nil {
				t.Fatal("compileBatch unexpectedly succeeded")
			}
		})
	}
}

func TestBatchBounds(t *testing.T) {
	now := time.Now()
	limits := DefaultLimits()
	tests := []Batch{
		{},
		{Actions: make([]Action, MaxActions+1)},
		{Actions: []Action{{Type: ActionWait, Duration: MaxWaitDuration + time.Nanosecond}}},
		{Actions: []Action{{Type: ActionWait, Duration: 5 * time.Second}, {Type: ActionWait, Duration: 5 * time.Second}, {Type: ActionWait, Duration: time.Nanosecond}}},
		{Actions: []Action{{Type: ActionScreenshot}, {Type: ActionWait, Duration: time.Millisecond}}},
		{Actions: []Action{{Type: ActionDrag, Path: []Point{{}, {X: 1}}, Button: "invalid"}}},
		{Actions: []Action{{Type: ActionScroll, DeltaY: 128}}},
	}
	for index, batch := range tests {
		if _, err := compileBatch(batch, limits, 7, now); !errors.Is(err, ErrInvalidAction) {
			t.Errorf("case %d error = %v", index, err)
		}
	}
}

func TestBatchDurationIncludesTyping(t *testing.T) {
	limits := DefaultLimits()
	limits.KeyHold = time.Second
	limits.InterKey = time.Second
	_, err := compileBatch(Batch{Actions: []Action{{Type: ActionTypeText, Text: "12345678"}}}, limits, 7, time.Now())
	if !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("compileBatch error = %v", err)
	}
}

func FuzzCompileCoordinate(f *testing.F) {
	f.Add(0, 0, 1, 1)
	f.Add(1919, 1079, 1920, 1080)
	f.Add(-1, 0, 1920, 1080)
	f.Fuzz(func(t *testing.T, x, y, width, height int) {
		now := time.Unix(1, 0)
		batch := Batch{
			Observation: &ObservationBinding{ID: "obs", Generation: 1, Width: width, Height: height, CapturedAt: now},
			Actions:     []Action{{Type: ActionMove, X: x, Y: y}},
		}
		compiled, err := compileBatch(batch, DefaultLimits(), 1, now)
		valid := width > 0 && height > 0 && x >= 0 && x < width && y >= 0 && y < height
		if valid && err != nil {
			t.Fatalf("valid coordinate rejected: %v", err)
		}
		if !valid && err == nil {
			t.Fatal("invalid coordinate accepted")
		}
		if err == nil {
			point := compiled.actions[0].point
			if point.X < 0 || point.X > absoluteCoordinate || point.Y < 0 || point.Y > absoluteCoordinate {
				t.Fatalf("mapped point out of range: %+v", point)
			}
		}
	})
}
