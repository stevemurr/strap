package tui

import (
	"bytes"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
)

const (
	keyboardPush = "\x1b[>1u"
	keyboardPop  = "\x1b[<u"
	enterScreen  = "\x1b[?1049h"
	leaveScreen  = "\x1b[?1049l"
)

// Enable disambiguated keys on the alternate screen only. Coupling this to
// Bubble Tea's screen lifecycle also restores keyboard state on exit, panic,
// and terminal release, and re-enables it when the terminal is restored.
// https://sw.kovidgoyal.net/kitty/keyboard-protocol/#progressive-enhancement
func composerOutput() io.Writer {
	output := terminalOutput(nil)
	if f, ok := output.(*frameWriter); ok {
		return keyboardWriter{File: f}
	}
	return output
}

type keyboardWriter struct{ term.File }

func (w keyboardWriter) Write(p []byte) (int, error) {
	encoded := bytes.ReplaceAll(p, []byte(enterScreen), []byte(enterScreen+keyboardPush))
	encoded = bytes.ReplaceAll(encoded, []byte(leaveScreen), []byte(keyboardPop+leaveScreen))
	n, err := w.File.Write(encoded)
	if n != len(encoded) {
		if err == nil {
			err = io.ErrShortWrite
		}
		return 0, err
	}
	return len(p), err
}

func decodeTerminalKey(sequence string) (tea.KeyMsg, bool) {
	if !strings.HasPrefix(sequence, "\x1b[") || len(sequence) < 4 {
		return tea.KeyMsg{}, false
	}
	fields := strings.Split(sequence[2:len(sequence)-1], ";")
	var codeText, modifierText string
	switch sequence[len(sequence)-1] {
	case 'u': // Kitty / CSI-u.
		if len(fields) > 2 {
			return tea.KeyMsg{}, false
		}
		codeText, modifierText = fields[0], "1"
		if len(fields) == 2 {
			modifierText = fields[1]
		}
	case '~': // xterm modifyOtherKeys, when configured by the terminal.
		if len(fields) != 3 || fields[0] != "27" {
			return tea.KeyMsg{}, false
		}
		codeText, modifierText = fields[2], fields[1]
	default:
		return tea.KeyMsg{}, false
	}
	code, err := strconv.Atoi(codeText)
	if err != nil || code < 0 || code > utf8.MaxRune {
		return tea.KeyMsg{}, false
	}
	modifier, err := strconv.Atoi(modifierText)
	if err != nil || modifier < 1 || modifier > 256 {
		return tea.KeyMsg{}, false
	}
	mods := (modifier - 1) &^ (64 | 128) // Caps/Num Lock do not change bindings.
	if mods & ^7 != 0 {                  // Do not turn Super/Hyper/Meta shortcuts into typing.
		return tea.KeyMsg{}, false
	}
	shift, alt, ctrl := mods&1 != 0, mods&2 != 0, mods&4 != 0
	key := tea.KeyMsg{Alt: alt}
	// Disambiguation also gives keypad keys distinct codes. Preserve their
	// normal editing behavior rather than dropping them when enabling the mode.
	if code >= 57399 && code <= 57416 {
		code = int([]rune("0123456789./*-+\r=,")[code-57399])
	}
	if code >= 57417 && code <= 57426 {
		plain := [...]tea.KeyType{tea.KeyLeft, tea.KeyRight, tea.KeyUp, tea.KeyDown, tea.KeyPgUp, tea.KeyPgDown, tea.KeyHome, tea.KeyEnd, tea.KeyInsert, tea.KeyDelete}
		control := [...]tea.KeyType{tea.KeyCtrlLeft, tea.KeyCtrlRight, tea.KeyCtrlUp, tea.KeyCtrlDown, tea.KeyCtrlPgUp, tea.KeyCtrlPgDown, tea.KeyCtrlHome, tea.KeyCtrlEnd, tea.KeyInsert, tea.KeyDelete}
		key.Type = plain[code-57417]
		if ctrl {
			key.Type = control[code-57417]
		}
		if shift {
			shifted := map[tea.KeyType]tea.KeyType{
				tea.KeyLeft: tea.KeyShiftLeft, tea.KeyRight: tea.KeyShiftRight, tea.KeyUp: tea.KeyShiftUp, tea.KeyDown: tea.KeyShiftDown,
				tea.KeyHome: tea.KeyShiftHome, tea.KeyEnd: tea.KeyShiftEnd,
				tea.KeyCtrlLeft: tea.KeyCtrlShiftLeft, tea.KeyCtrlRight: tea.KeyCtrlShiftRight, tea.KeyCtrlUp: tea.KeyCtrlShiftUp, tea.KeyCtrlDown: tea.KeyCtrlShiftDown,
				tea.KeyCtrlHome: tea.KeyCtrlShiftHome, tea.KeyCtrlEnd: tea.KeyCtrlShiftEnd,
			}
			if kind, ok := shifted[key.Type]; ok {
				key.Type = kind
			}
		}
		return key, true
	}
	switch code {
	case 13:
		if ctrl {
			return tea.KeyMsg{}, false
		}
		key.Type = tea.KeyEnter
		// v1 has no Shift bit. Use the existing newline binding.
		key.Alt = alt || shift
	case 9:
		key.Type = tea.KeyTab
		if shift {
			key.Type = tea.KeyShiftTab
		}
	case 127:
		key.Type = tea.KeyBackspace
	case 27:
		key.Type = tea.KeyEsc
	default:
		r := rune(code)
		if ctrl {
			r = unicode.ToUpper(r)
			switch r {
			case ' ', '2':
				r = '@'
			case '3':
				r = '['
			case '4':
				r = '\\'
			case '5':
				r = ']'
			case '6', '~':
				r = '^'
			case '7', '/':
				r = '_'
			case '8', '?':
				key.Type = tea.KeyBackspace
				return key, true
			}
			if r < '@' || r > '_' {
				return tea.KeyMsg{}, false
			}
			key.Type = tea.KeyType(r & 31)
		} else {
			if !unicode.IsPrint(r) || (r >= 0xe000 && r <= 0xf8ff) {
				return tea.KeyMsg{}, false
			}
			if shift {
				r = unicode.ToUpper(r)
			}
			key.Type, key.Runes = tea.KeyRunes, []rune{r}
		}
	}
	return key, true
}
