package input

import (
	"errors"
	"strings"
	"testing"
)

func TestCompileTextUSLayout(t *testing.T) {
	t.Parallel()
	printable := ""
	for value := rune(0x20); value <= 0x7e; value++ {
		printable += string(value)
	}
	strokes, err := CompileText(printable)
	if err != nil {
		t.Fatal(err)
	}
	if len(strokes) != 95 {
		t.Fatalf("stroke count = %d", len(strokes))
	}
	if strokes[0] != (Keystroke{Key: 0x2c}) {
		t.Fatalf("space stroke = %+v", strokes[0])
	}
	capital, err := CompileText("A!")
	if err != nil {
		t.Fatal(err)
	}
	if capital[0] != (Keystroke{Modifier: 2, Key: 4}) || capital[1] != (Keystroke{Modifier: 2, Key: 0x1e}) {
		t.Fatalf("capital strokes = %+v", capital)
	}
}

func TestCompileTextRejectsWholeInput(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"", "ok\n", "ok\t", "café", string([]byte{0xff})} {
		strokes, err := CompileText(input)
		if !errors.Is(err, ErrUnsupportedText) || strokes != nil {
			t.Fatalf("CompileText(%q) = %v, %v", input, strokes, err)
		}
	}
	strokes, err := CompileText(strings.Repeat("a", MaxTextRunes+1))
	if !errors.Is(err, ErrUnsupportedText) || strokes != nil {
		t.Fatalf("oversized CompileText = %v, %v", strokes, err)
	}
}

func TestCompileKeyCombo(t *testing.T) {
	t.Parallel()
	modifier, keys, err := CompileKeyCombo([]string{"ctrl", "alt-left", "Delete"})
	if err != nil {
		t.Fatal(err)
	}
	if modifier != 0x05 || len(keys) != 1 || keys[0] != 0x4c {
		t.Fatalf("combo = modifier %x keys %x", modifier, keys)
	}
	modifier, keys, err = CompileKeyCombo([]string{"ShiftRight"})
	if err != nil || modifier != 0x20 || len(keys) != 0 {
		t.Fatalf("modifier-only combo = %x %x %v", modifier, keys, err)
	}
	for _, names := range [][]string{nil, {"unknown"}, {"A", "A"}, {"ctrl", "control"}} {
		if _, _, err := CompileKeyCombo(names); !errors.Is(err, ErrUnknownKey) {
			t.Fatalf("CompileKeyCombo(%v) error = %v", names, err)
		}
	}
}

func FuzzCompileText(f *testing.F) {
	f.Add("Hello, world!")
	f.Add("bad\n")
	f.Add("你好")
	f.Fuzz(func(t *testing.T, value string) {
		strokes, err := CompileText(value)
		if err == nil && len(strokes) != len([]rune(value)) {
			t.Fatalf("stroke count %d does not match rune count", len(strokes))
		}
		if err != nil && strokes != nil {
			t.Fatal("failed compilation returned a partial program")
		}
	})
}

func TestCommandAbbreviation(t *testing.T) {
	modifier, keys, err := CompileKeyCombo([]string{"CMD", "A"})
	if err != nil || modifier != 0x08 || len(keys) != 1 || keys[0] != 0x04 {
		t.Fatalf("CMD+A: %x %x %v", modifier, keys, err)
	}
}
