package jetkvm

import (
	"testing"
)

func TestRPCMuxIgnoresOldGenerationResponse(t *testing.T) {
	t.Parallel()

	mux := newRPCMux(8)
	id, response, err := mux.register()
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"jsonrpc":"2.0","result":"pong","id":"` + id + `"}`)
	mux.deliver(7, payload)
	select {
	case <-response:
		t.Fatal("old generation response was delivered")
	default:
	}
	mux.deliver(8, payload)
	select {
	case outcome := <-response:
		if outcome.err != nil || string(outcome.result) != `"pong"` {
			t.Fatalf("outcome = (%s, %v)", outcome.result, outcome.err)
		}
	default:
		t.Fatal("current generation response was not delivered")
	}
}
