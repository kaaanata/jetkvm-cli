// Package input implements JetKVM HID-RPC encoding and bounded input execution.
package input

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// HID-RPC v1 message types observed in the JetKVM 0.5.8/dev protocol.
const (
	HIDRPCVersion byte = 0x01

	messageHandshake      byte = 0x01
	messageKeyboardReport byte = 0x02
	messagePointerReport  byte = 0x03
	messageKeypressReport byte = 0x05
	messageMouseReport    byte = 0x06
)

const (
	keyboardSlots      = 6
	absoluteCoordinate = 32767
	maxMouseButtons    = 0x1f
)

var ErrInvalidProtocolValue = errors.New("invalid HID protocol value")

// Handshake marshals the HID-RPC v1 handshake.
func Handshake() []byte {
	return []byte{messageHandshake, HIDRPCVersion}
}

// ParseHandshake validates a device handshake and returns its negotiated version.
func ParseHandshake(data []byte) (byte, error) {
	if len(data) != 2 || data[0] != messageHandshake || data[1] == 0 || data[1] > HIDRPCVersion {
		return 0, fmt.Errorf("%w: unsupported handshake", ErrInvalidProtocolValue)
	}
	return data[1], nil
}

// KeyboardReport marshals one complete boot-keyboard state. Unused slots are zeroed.
func KeyboardReport(modifier byte, keys ...byte) ([]byte, error) {
	if len(keys) > keyboardSlots {
		return nil, fmt.Errorf("%w: keyboard report contains %d keys, maximum is %d", ErrInvalidProtocolValue, len(keys), keyboardSlots)
	}
	report := make([]byte, 2+keyboardSlots)
	report[0] = messageKeyboardReport
	report[1] = modifier
	copy(report[2:], keys)
	return report, nil
}

// KeypressReport marshals a single key transition.
func KeypressReport(key byte, pressed bool) ([]byte, error) {
	if key == 0 {
		return nil, fmt.Errorf("%w: key usage must be non-zero", ErrInvalidProtocolValue)
	}
	pressedByte := byte(0)
	if pressed {
		pressedByte = 1
	}
	return []byte{messageKeypressReport, key, pressedByte}, nil
}

// PointerReport marshals absolute coordinates in JetKVM's 0..32767 HID space.
func PointerReport(x, y int, buttons ButtonMask) ([]byte, error) {
	if x < 0 || x > absoluteCoordinate || y < 0 || y > absoluteCoordinate {
		return nil, fmt.Errorf("%w: absolute pointer coordinates must be within 0..%d", ErrInvalidProtocolValue, absoluteCoordinate)
	}
	if buttons&^ButtonMask(maxMouseButtons) != 0 {
		return nil, fmt.Errorf("%w: unsupported pointer button mask 0x%x", ErrInvalidProtocolValue, byte(buttons))
	}
	report := make([]byte, 10)
	report[0] = messagePointerReport
	binary.BigEndian.PutUint32(report[1:5], uint32(x))
	binary.BigEndian.PutUint32(report[5:9], uint32(y))
	report[9] = byte(buttons)
	return report, nil
}

// RelativeMouseReport marshals one relative pointer update.
func RelativeMouseReport(dx, dy int, buttons ButtonMask) ([]byte, error) {
	if dx < -128 || dx > 127 || dy < -128 || dy > 127 {
		return nil, fmt.Errorf("%w: relative movement must be within -128..127", ErrInvalidProtocolValue)
	}
	if buttons&^ButtonMask(maxMouseButtons) != 0 {
		return nil, fmt.Errorf("%w: unsupported pointer button mask 0x%x", ErrInvalidProtocolValue, byte(buttons))
	}
	return []byte{messageMouseReport, byte(int8(dx)), byte(int8(dy)), byte(buttons)}, nil
}
