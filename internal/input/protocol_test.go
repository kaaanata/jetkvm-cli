package input

import (
	"bytes"
	"errors"
	"testing"
)

func TestHIDRPCWireEncoding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		got  func() ([]byte, error)
		want []byte
	}{
		{name: "keyboard", got: func() ([]byte, error) { return KeyboardReport(2, 4) }, want: []byte{2, 2, 4, 0, 0, 0, 0, 0}},
		{name: "keypress", got: func() ([]byte, error) { return KeypressReport(4, true) }, want: []byte{5, 4, 1}},
		{name: "absolute pointer", got: func() ([]byte, error) { return PointerReport(0x1234, 0x5678, buttonLeftMask) }, want: []byte{3, 0, 0, 0x12, 0x34, 0, 0, 0x56, 0x78, 1}},
		{name: "relative pointer", got: func() ([]byte, error) { return RelativeMouseReport(-1, 127, buttonRightMask) }, want: []byte{6, 0xff, 0x7f, 2}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.got()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, test.want) {
				t.Fatalf("wire = %v, want %v", got, test.want)
			}
		})
	}
}

func TestHandshakeValidation(t *testing.T) {
	t.Parallel()
	if got := Handshake(); !bytes.Equal(got, []byte{1, 1}) {
		t.Fatalf("Handshake() = %v", got)
	}
	if version, err := ParseHandshake([]byte{1, 1}); err != nil || version != 1 {
		t.Fatalf("ParseHandshake() = %d, %v", version, err)
	}
	for _, input := range [][]byte{nil, {1}, {1, 0}, {1, 2}, {2, 1}, {1, 1, 0}} {
		if _, err := ParseHandshake(input); !errors.Is(err, ErrInvalidProtocolValue) {
			t.Fatalf("ParseHandshake(%v) error = %v", input, err)
		}
	}
}

func TestProtocolBounds(t *testing.T) {
	t.Parallel()
	if _, err := KeyboardReport(0, 1, 2, 3, 4, 5, 6, 7); !errors.Is(err, ErrInvalidProtocolValue) {
		t.Fatalf("KeyboardReport error = %v", err)
	}
	if _, err := PointerReport(-1, 0, 0); !errors.Is(err, ErrInvalidProtocolValue) {
		t.Fatalf("PointerReport error = %v", err)
	}
	if _, err := RelativeMouseReport(128, 0, 0); !errors.Is(err, ErrInvalidProtocolValue) {
		t.Fatalf("RelativeMouseReport error = %v", err)
	}
}

func FuzzPointerReport(f *testing.F) {
	f.Add(0, 0, byte(0))
	f.Add(32767, 32767, byte(31))
	f.Add(-1, 32768, byte(255))
	f.Fuzz(func(t *testing.T, x, y int, buttons byte) {
		report, err := PointerReport(x, y, ButtonMask(buttons))
		valid := x >= 0 && x <= absoluteCoordinate && y >= 0 && y <= absoluteCoordinate && buttons <= maxMouseButtons
		if valid && (err != nil || len(report) != 10) {
			t.Fatalf("valid report = %v, %v", report, err)
		}
		if !valid && !errors.Is(err, ErrInvalidProtocolValue) {
			t.Fatalf("invalid report error = %v", err)
		}
	})
}
