package automation

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/kaaanata/jetkvm-cli/internal/input"
)

func TestSessionAdapterCrossesLedgerBoundaryOnceAndFlushes(t *testing.T) {
	protocol := &fakeProtocolSession{generation: 41}
	adapter := newTestSessionAdapter(t, protocol, 7)
	starts := 0
	receipt, started, err := adapter.RunActions(t.Context(), input.Batch{
		Actions: []input.Action{{Type: input.ActionKeypress, Keys: []string{"ENTER"}}},
	}, func(context.Context) error {
		starts++
		return nil
	})
	if err != nil {
		t.Fatalf("RunActions() error = %v", err)
	}
	if !started || starts != 1 || !receipt.Neutralized {
		t.Fatalf("started = %v, starts = %d, receipt = %+v", started, starts, receipt)
	}
	protocol.mu.Lock()
	defer protocol.mu.Unlock()
	if len(protocol.hid) != 5 || protocol.flushes != 1 {
		t.Fatalf("HID sends = %d, flushes = %d", len(protocol.hid), protocol.flushes)
	}
}

func TestSessionAdapterDoesNotSendWhenLedgerBoundaryFails(t *testing.T) {
	protocol := &fakeProtocolSession{generation: 9}
	adapter := newTestSessionAdapter(t, protocol, 2)
	ledgerErr := errors.New("ledger unavailable")
	_, started, err := adapter.RunActions(t.Context(), input.Batch{
		Actions: []input.Action{{Type: input.ActionKeypress, Keys: []string{"ENTER"}}},
	}, func(context.Context) error { return ledgerErr })
	if !errors.Is(err, ledgerErr) || started {
		t.Fatalf("RunActions() error = %v, started = %v", err, started)
	}
	protocol.mu.Lock()
	defer protocol.mu.Unlock()
	if len(protocol.hid) != 0 {
		t.Fatalf("ledger failure sent %d HID messages", len(protocol.hid))
	}
}

func TestSessionAdapterFinalGenerationFenceAndClose(t *testing.T) {
	protocol := &fakeProtocolSession{generation: 12}
	adapter := newTestSessionAdapter(t, protocol, 3)
	if err := adapter.SendHID(t.Context(), 4, input.Reliable, []byte{1}); !errors.Is(err, input.ErrStaleGeneration) {
		t.Fatalf("SendHID() error = %v, want stale generation", err)
	}
	if err := adapter.Close(t.Context()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	protocol.mu.Lock()
	if len(protocol.hid) != 3 || protocol.flushes != 1 || !protocol.closed {
		t.Fatalf("protocol after close = %+v", protocol)
	}
	protocol.mu.Unlock()
	if err := adapter.Close(t.Context()); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func newTestSessionAdapter(t *testing.T, protocol protocolSession, generation uint64) *sessionAdapter {
	t.Helper()
	adapter := &sessionAdapter{generation: generation, protocol: protocol}
	manager, err := input.NewManager(input.ManagerConfig{Transport: adapter, Generation: generation})
	if err != nil {
		t.Fatal(err)
	}
	adapter.input = manager
	return adapter
}

type fakeProtocolSession struct {
	mu         sync.Mutex
	generation uint64
	hid        [][]byte
	flushes    int
	closed     bool
}

func (s *fakeProtocolSession) Generation() uint64 { return s.generation }

func (s *fakeProtocolSession) SendHIDForGeneration(_ context.Context, generation uint64, payload []byte) error {
	if generation != s.generation {
		return errors.New("protocol generation mismatch")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hid = append(s.hid, append([]byte(nil), payload...))
	return nil
}

func (s *fakeProtocolSession) FlushHID(_ context.Context, generation uint64) error {
	if generation != s.generation {
		return errors.New("protocol generation mismatch")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushes++
	return nil
}

func (s *fakeProtocolSession) CallRPC(context.Context, string, any, any) error { return nil }

func (s *fakeProtocolSession) CloseContext(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

var _ protocolSession = (*fakeProtocolSession)(nil)
