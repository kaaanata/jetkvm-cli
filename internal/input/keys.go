package input

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

const MaxTextRunes = 4096

var (
	ErrUnknownKey      = errors.New("unknown key")
	ErrUnsupportedText = errors.New("text contains a character unsupported by the US keyboard layout")
)

type Keystroke struct {
	Modifier byte
	Key      byte
}

var keyUsages = buildKeyUsages()

var modifierUsages = map[string]byte{
	"CTRL":         0x01,
	"CONTROL":      0x01,
	"CTRLLEFT":     0x01,
	"CONTROLLEFT":  0x01,
	"SHIFT":        0x02,
	"SHIFTLEFT":    0x02,
	"ALT":          0x04,
	"ALTLEFT":      0x04,
	"META":         0x08,
	"SUPER":        0x08,
	"COMMAND":      0x08,
	"METALEFT":     0x08,
	"SUPERLEFT":    0x08,
	"CONTROLRIGHT": 0x10,
	"CTRLRIGHT":    0x10,
	"SHIFTRIGHT":   0x20,
	"ALTRIGHT":     0x40,
	"ALTGR":        0x40,
	"METARIGHT":    0x80,
	"SUPERRIGHT":   0x80,
	"COMMANDRIGHT": 0x80,
}

// CompileKeyCombo resolves a named chord to a complete keyboard report.
// Names are case-insensitive and ignore '-', '_', and spaces.
func CompileKeyCombo(names []string) (modifier byte, keys []byte, err error) {
	if len(names) == 0 {
		return 0, nil, fmt.Errorf("%w: key combination is empty", ErrUnknownKey)
	}
	seenKeys := make(map[byte]struct{})
	seenModifiers := byte(0)
	for _, name := range names {
		normalized := normalizeKeyName(name)
		if normalized == "" {
			return 0, nil, fmt.Errorf("%w: empty key name", ErrUnknownKey)
		}
		if mask, ok := modifierUsages[normalized]; ok {
			if seenModifiers&mask != 0 {
				return 0, nil, fmt.Errorf("%w: duplicate modifier %q", ErrUnknownKey, name)
			}
			seenModifiers |= mask
			continue
		}
		usage, ok := keyUsages[normalized]
		if !ok {
			return 0, nil, fmt.Errorf("%w: %q", ErrUnknownKey, name)
		}
		if _, duplicate := seenKeys[usage]; duplicate {
			return 0, nil, fmt.Errorf("%w: duplicate key %q", ErrUnknownKey, name)
		}
		seenKeys[usage] = struct{}{}
		keys = append(keys, usage)
	}
	if len(keys) > keyboardSlots {
		return 0, nil, fmt.Errorf("%w: key combination exceeds %d non-modifier keys", ErrUnknownKey, keyboardSlots)
	}
	if seenModifiers == 0 && len(keys) == 0 {
		return 0, nil, fmt.Errorf("%w: key combination is empty", ErrUnknownKey)
	}
	return seenModifiers, slices.Clone(keys), nil
}

// CompileText completely validates and translates printable US-layout text.
// It returns no partial result when any rune is unsupported.
func CompileText(text string) ([]Keystroke, error) {
	if !utf8.ValidString(text) {
		return nil, fmt.Errorf("%w: invalid UTF-8", ErrUnsupportedText)
	}
	count := utf8.RuneCountInString(text)
	if count == 0 || count > MaxTextRunes {
		return nil, fmt.Errorf("%w: text length must be within 1..%d runes", ErrUnsupportedText, MaxTextRunes)
	}
	strokes := make([]Keystroke, 0, count)
	for _, value := range text {
		stroke, ok := usRune(value)
		if !ok {
			return nil, fmt.Errorf("%w: U+%04X", ErrUnsupportedText, value)
		}
		strokes = append(strokes, stroke)
	}
	return strokes, nil
}

func normalizeKeyName(name string) string {
	name = strings.ToUpper(strings.TrimSpace(name))
	replacer := strings.NewReplacer("-", "", "_", "", " ", "")
	return replacer.Replace(name)
}

func buildKeyUsages() map[string]byte {
	keys := map[string]byte{
		"ENTER": 0x28, "RETURN": 0x28, "ESCAPE": 0x29, "ESC": 0x29,
		"BACKSPACE": 0x2a, "TAB": 0x2b, "SPACE": 0x2c,
		"MINUS": 0x2d, "EQUAL": 0x2e, "BRACKETLEFT": 0x2f, "BRACKETRIGHT": 0x30,
		"BACKSLASH": 0x31, "SEMICOLON": 0x33, "QUOTE": 0x34, "BACKQUOTE": 0x35,
		"COMMA": 0x36, "PERIOD": 0x37, "SLASH": 0x38, "CAPSLOCK": 0x39,
		"PRINTSCREEN": 0x46, "SCROLLLOCK": 0x47, "PAUSE": 0x48, "INSERT": 0x49,
		"HOME": 0x4a, "PAGEUP": 0x4b, "DELETE": 0x4c, "END": 0x4d,
		"PAGEDOWN": 0x4e, "ARROWRIGHT": 0x4f, "RIGHT": 0x4f, "ARROWLEFT": 0x50,
		"LEFT": 0x50, "ARROWDOWN": 0x51, "DOWN": 0x51, "ARROWUP": 0x52, "UP": 0x52,
		"NUMLOCK": 0x53, "NUMPADDIVIDE": 0x54, "NUMPADMULTIPLY": 0x55,
		"NUMPADSUBTRACT": 0x56, "NUMPADADD": 0x57, "NUMPADENTER": 0x58,
		"NUMPADDECIMAL": 0x63, "APPLICATION": 0x65, "CONTEXTMENU": 0x65,
	}
	for index := range 26 {
		usage := byte(0x04 + index)
		letter := string(rune('A' + index))
		keys[letter] = usage
		keys["KEY"+letter] = usage
	}
	for digit := range 10 {
		usage := byte(0x27)
		if digit != 0 {
			usage = byte(0x1d + digit)
		}
		name := fmt.Sprintf("%d", digit)
		keys[name] = usage
		keys["DIGIT"+name] = usage
	}
	for index := range 24 {
		usage := byte(0x3a + index)
		if index >= 12 {
			usage = byte(0x68 + index - 12)
		}
		keys[fmt.Sprintf("F%d", index+1)] = usage
	}
	for digit := range 10 {
		usage := byte(0x62)
		if digit != 0 {
			usage = byte(0x58 + digit)
		}
		keys[fmt.Sprintf("NUMPAD%d", digit)] = usage
	}
	return keys
}

func usRune(value rune) (Keystroke, bool) {
	if value >= 'a' && value <= 'z' {
		return Keystroke{Key: byte(0x04 + value - 'a')}, true
	}
	if value >= 'A' && value <= 'Z' {
		return Keystroke{Modifier: 0x02, Key: byte(0x04 + value - 'A')}, true
	}
	if value >= '1' && value <= '9' {
		return Keystroke{Key: byte(0x1e + value - '1')}, true
	}
	if value == '0' {
		return Keystroke{Key: 0x27}, true
	}
	if stroke, ok := usPunctuation[value]; ok {
		return stroke, true
	}
	return Keystroke{}, false
}

var usPunctuation = map[rune]Keystroke{
	' ': {Key: 0x2c}, '-': {Key: 0x2d}, '_': {Modifier: 0x02, Key: 0x2d},
	'=': {Key: 0x2e}, '+': {Modifier: 0x02, Key: 0x2e}, '[': {Key: 0x2f},
	'{': {Modifier: 0x02, Key: 0x2f}, ']': {Key: 0x30}, '}': {Modifier: 0x02, Key: 0x30},
	'\\': {Key: 0x31}, '|': {Modifier: 0x02, Key: 0x31}, ';': {Key: 0x33},
	':': {Modifier: 0x02, Key: 0x33}, '\'': {Key: 0x34}, '"': {Modifier: 0x02, Key: 0x34},
	'`': {Key: 0x35}, '~': {Modifier: 0x02, Key: 0x35}, ',': {Key: 0x36},
	'<': {Modifier: 0x02, Key: 0x36}, '.': {Key: 0x37}, '>': {Modifier: 0x02, Key: 0x37},
	'/': {Key: 0x38}, '?': {Modifier: 0x02, Key: 0x38}, '!': {Modifier: 0x02, Key: 0x1e},
	'@': {Modifier: 0x02, Key: 0x1f}, '#': {Modifier: 0x02, Key: 0x20},
	'$': {Modifier: 0x02, Key: 0x21}, '%': {Modifier: 0x02, Key: 0x22},
	'^': {Modifier: 0x02, Key: 0x23}, '&': {Modifier: 0x02, Key: 0x24},
	'*': {Modifier: 0x02, Key: 0x25}, '(': {Modifier: 0x02, Key: 0x26},
	')': {Modifier: 0x02, Key: 0x27},
}
