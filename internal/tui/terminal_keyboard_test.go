package tui

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type keyboardProbe struct {
	input *model
	keys  []tea.KeyMsg
}

func (*keyboardProbe) Init() tea.Cmd { return nil }
func (*keyboardProbe) View() string  { return "" }
func (p *keyboardProbe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		if key.Type == tea.KeyCtrlD {
			return p, tea.Quit
		}
		p.keys = append(p.keys, key)
		if p.input != nil {
			p.input.Update(key)
		}
	}
	return p, nil
}

func readKeyboard(t *testing.T, probe *keyboardProbe, raw string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	reader := &terminalInputReader{source: strings.NewReader(raw + "\x04")}
	_, err := tea.NewProgram(probe, tea.WithInput(reader), tea.WithOutput(io.Discard),
		tea.WithoutRenderer(), tea.WithoutSignalHandler(), tea.WithFilter(reader.filter), tea.WithContext(ctx)).Run()
	if err != nil {
		t.Fatal(err)
	}
}

func TestShiftEnterThroughTerminalReader(t *testing.T) {
	for _, sequence := range []string{"\x1b[13;2u", "\x1b[27;2;13~", "\x1b[13;66u", "\x1b[57414;2u"} {
		t.Run(sequence, func(t *testing.T) {
			m, s := setup(t)
			readKeyboard(t, &keyboardProbe{input: m}, "first"+sequence+"second")
			if m.input.Value() != "first\nsecond" || m.input.Height() != 2 || len(s.sent) != 0 {
				t.Fatalf("Shift+Enter did not insert a newline: draft=%q sent=%v", m.input.Value(), s.sent)
			}
			readKeyboard(t, &keyboardProbe{input: m}, "\r")
			if len(s.sent) != 1 || s.sent[0] != "first\nsecond" {
				t.Fatal("Enter did not send the complete draft", s.sent)
			}
		})
	}
}

func TestEnhancedKeysPreserveShortcutsAndPaste(t *testing.T) {
	for _, tc := range []struct{ raw, key string }{
		{"\x1b[27u", "esc"}, {"\x1b[99;5u", "ctrl+c"}, {"\x1b[106;5u", "ctrl+j"},
		{"\x1b[112;5u", "ctrl+p"}, {"\x1b[118;5u", "ctrl+v"}, {"\x1b[13;3u", "alt+enter"},
		{"\x1b[9;2u", "shift+tab"}, {"\x1b[1;3A", "alt+up"}, {"\x1b[127u", "backspace"},
		{"\x1b[97;3u", "alt+a"}, {"\x1b[97;4u", "alt+A"}, {"\x1b[27;5;106~", "ctrl+j"},
		{"\x1b[57417u", "left"}, {"\x1b[57418;6u", "ctrl+shift+right"}, {"\x1b[57423;5u", "ctrl+home"},
		{"\x1b[57400u", "1"}, {"\x1b[57413u", "+"}, {"\x1b[47;5u", "ctrl+_"},
	} {
		p := &keyboardProbe{}
		readKeyboard(t, p, tc.raw)
		if len(p.keys) != 1 || p.keys[0].String() != tc.key {
			t.Fatalf("%q: got %v, want %s", tc.raw, p.keys, tc.key)
		}
	}
	p := &keyboardProbe{}
	paste := "first\n\x1b[13;2u\nlast"
	readKeyboard(t, p, "\x1b[200~"+paste+"\x1b[201~")
	if len(p.keys) != 1 || !p.keys[0].Paste || string(p.keys[0].Runes) != paste {
		t.Fatalf("rewrote bracketed paste: %v", p.keys)
	}
	for _, raw := range []string{"\x1b[?1u", "\x1b[13;5u", "\x1b[13;2:3u", "\x1b[97;9u"} {
		p := &keyboardProbe{}
		readKeyboard(t, p, raw)
		if len(p.keys) != 0 {
			t.Fatalf("unexpected typing from non-key/unsupported event %q: %v", raw, p.keys)
		}
	}
}

func TestKeyboardModeFollowsAlternateScreenLifecycle(t *testing.T) {
	f := &frameTestFile{limit: -1}
	w := keyboardWriter{File: f}
	for range 2 { // Release and restore, then final exit.
		for _, text := range []string{enterScreen, "frame", leaveScreen} {
			if n, err := w.Write([]byte(text)); err != nil || n != len(text) {
				t.Fatalf("write: %d, %v", n, err)
			}
		}
	}
	want := strings.Repeat(enterScreen+keyboardPush+"frame"+keyboardPop+leaveScreen, 2)
	if f.String() != want || w.Fd() != f.Fd() {
		t.Fatalf("keyboard state or terminal descriptor not preserved: %q", f.String())
	}
}
