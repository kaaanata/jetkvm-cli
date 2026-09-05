package progress

import "testing"

func TestEventsAreOptionalAndRemainContextScoped(t *testing.T) {
	var got []Event
	ctx := WithObserver(t.Context(), func(e Event) { got = append(got, e) })
	Stage(t.Context(), "not subscribed")
	Stage(ctx, "Checking")
	Report(ctx, Event{Stage: "Downloading", Completed: 4, Total: 8})
	if len(got) != 2 || got[1].Completed != 4 || got[1].Total != 8 {
		t.Fatalf("events=%v", got)
	}
}
