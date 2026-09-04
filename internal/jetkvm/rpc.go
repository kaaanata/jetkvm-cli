package jetkvm

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

var (
	ErrRPCChannelUnavailable = errors.New("JetKVM RPC channel is unavailable")
	ErrHIDChannelUnavailable = errors.New("JetKVM HID channel is unavailable")
	ErrRPCProtocol           = errors.New("invalid JetKVM JSON-RPC response")
)

// RPCError is a JSON-RPC error returned by the JetKVM device.
type RPCError struct {
	Code    int
	Message string
	Data    jsontext.Value
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("JetKVM RPC error %d: %s", e.Code, e.Message)
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
	ID      string `json:"id"`
}

type rpcResponse struct {
	JSONRPC string         `json:"jsonrpc"`
	Result  jsontext.Value `json:"result,omitempty"`
	Error   *struct {
		Code    int            `json:"code"`
		Message string         `json:"message"`
		Data    jsontext.Value `json:"data,omitempty"`
	} `json:"error,omitempty"`
	ID string `json:"id"`
}

type rpcOutcome struct {
	result jsontext.Value
	err    error
}

type rpcMux struct {
	generation uint64
	nextID     atomic.Uint64
	mu         sync.Mutex
	pending    map[string]chan rpcOutcome
	closed     error
}

func newRPCMux(generation uint64) *rpcMux {
	return &rpcMux{generation: generation, pending: make(map[string]chan rpcOutcome)}
}

func (r *rpcMux) register() (string, <-chan rpcOutcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed != nil {
		return "", nil, r.closed
	}
	id := fmt.Sprintf("g%d-r%d", r.generation, r.nextID.Add(1))
	result := make(chan rpcOutcome, 1)
	r.pending[id] = result
	return id, result, nil
}

func (r *rpcMux) remove(id string) {
	r.mu.Lock()
	delete(r.pending, id)
	r.mu.Unlock()
}

func (r *rpcMux) deliver(generation uint64, payload []byte) {
	if generation != r.generation {
		return
	}
	var response rpcResponse
	if err := json.Unmarshal(payload, &response); err != nil || response.JSONRPC != "2.0" || response.ID == "" {
		return
	}

	r.mu.Lock()
	result := r.pending[response.ID]
	if result != nil {
		delete(r.pending, response.ID)
	}
	r.mu.Unlock()
	if result == nil {
		return
	}

	if response.Error != nil {
		result <- rpcOutcome{err: &RPCError{
			Code:    response.Error.Code,
			Message: response.Error.Message,
			Data:    response.Error.Data.Clone(),
		}}
		return
	}
	result <- rpcOutcome{result: response.Result.Clone()}
}

func (r *rpcMux) close(cause error) {
	r.mu.Lock()
	if r.closed != nil {
		r.mu.Unlock()
		return
	}
	r.closed = cause
	pending := r.pending
	r.pending = make(map[string]chan rpcOutcome)
	r.mu.Unlock()

	for _, result := range pending {
		result <- rpcOutcome{err: cause}
	}
}

func waitRPC(ctx context.Context, result <-chan rpcOutcome) (jsontext.Value, error) {
	select {
	case outcome := <-result:
		return outcome.result, outcome.err
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}
