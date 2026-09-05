package automation

import (
	"context"
	"errors"

	"github.com/kaaanata/jetkvm-cli/internal/jetkvm"
)

var (
	ErrVideoSleeping = errors.New("video capture is sleeping")
	ErrVideoNoSignal = errors.New("no HDMI signal; the host may be asleep, powered off, or disconnected")
)

// videoReadiness reads the firmware's own state without sending HID or changing
// persistent settings. Older firmware may omit these optional diagnostic RPCs.
func (s *sessionAdapter) videoReadiness(ctx context.Context) error {
	var sleep struct {
		Supported bool `json:"supported"`
		Enabled   bool `json:"enabled"`
	}
	if err := s.protocol.CallRPC(ctx, "getVideoSleepMode", nil, &sleep); err != nil {
		if rpc, ok := errors.AsType[*jetkvm.RPCError](err); ok && rpc.Code == -32601 {
			return nil
		}
		return err
	}
	if sleep.Supported && sleep.Enabled {
		return ErrVideoSleeping
	}
	var signal struct {
		Ready bool   `json:"ready"`
		Error string `json:"error"`
	}
	if err := s.protocol.CallRPC(ctx, "getVideoState", nil, &signal); err != nil {
		if rpc, ok := errors.AsType[*jetkvm.RPCError](err); ok && rpc.Code == -32601 {
			return nil
		}
		return err
	}
	if !signal.Ready && signal.Error == "no_signal" {
		return ErrVideoNoSignal
	}
	return nil
}
