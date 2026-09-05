// Package progress carries ephemeral observations, never operation authority.
package progress

import "context"

type Event struct {
	Stage     string
	Completed int64
	Total     int64 // Zero means unknown; never synthesize a percentage.
}

type Observer func(Event)
type key struct{}

func WithObserver(ctx context.Context, observer Observer) context.Context {
	return context.WithValue(ctx, key{}, observer)
}

func Report(ctx context.Context, event Event) {
	if observer, ok := ctx.Value(key{}).(Observer); ok && observer != nil {
		observer(event)
	}
}

func Stage(ctx context.Context, stage string) { Report(ctx, Event{Stage: stage}) }
