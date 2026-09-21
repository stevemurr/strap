package tui

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type inputChunks []string

func (r *inputChunks) Read(p []byte) (int, error) {
	if len(*r) == 0 {
		return 0, io.EOF
	}
	n := copy(p, (*r)[0])
	(*r)[0] = (*r)[0][n:]
	if (*r)[0] == "" {
		*r = (*r)[1:]
	}
	return n, nil
}

func readFramedKeyboard(t *testing.T, chunks ...string) *keyboardProbe {
	t.Helper()
	m, _ := setup(t)
	probe := &keyboardProbe{input: m}
	source := inputChunks(append(chunks, "\x04"))
	reader := &terminalInputReader{source: &source, escapeReady: func() (bool, error) { return true, nil }}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := tea.NewProgram(probe, tea.WithInput(reader), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler(), tea.WithFilter(reader.filter), tea.WithContext(ctx)).Run()
	if err != nil {
		t.Fatal(err)
	}
	return probe
}

func TestFragmentedTerminalReportsDoNotType(t *testing.T) {
	for _, report := range []string{"\x1b[<35;8;12M", "\x1b[<0;358;42m", "\x1b[35;8R", "\x1b[?1u", "\x1b[M !!"} {
		for split := 1; split < len(report); split++ {
			p := readFramedKeyboard(t, "before", report[:split], report[split:], "after")
			if got := p.input.input.Value(); got != "beforeafter" {
				t.Fatalf("report %q split %d: draft %q", report, split, got)
			}
		}
	}
}

func TestFragmentedKeyboardAndUnicode(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"\x1b[13;2u", "first\nlast"},
		{"\x1b[27;2;13~", "first\nlast"},
		{"é👋こんにちは", "firsté👋こんにちはlast"},
		{"\x1b[D", "firslastt"},
	} {
		for split := 1; split < len(tc.raw); split++ {
			p := readFramedKeyboard(t, "first", tc.raw[:split], tc.raw[split:], "last")
			if got := p.input.input.Value(); got != tc.want {
				t.Fatalf("%q split %d: %q, want %q", tc.raw, split, got, tc.want)
			}
		}
	}
}

func TestFragmentedPastePreservesLiteralSequences(t *testing.T) {
	text := "hello é👋\n[&358\n\x1b[13;2u\nlast"
	raw := "\x1b[200~" + text + "\x1b[201~"
	for split := 1; split < len(raw); split++ {
		p := readFramedKeyboard(t, raw[:split], raw[split:])
		if len(p.keys) != 1 || !p.keys[0].Paste || string(p.keys[0].Runes) != text {
			t.Fatalf("split %d: keys=%v", split, p.keys)
		}
	}
	chunks := make([]string, len(raw))
	// Iterate bytes, including UTF-8 continuation bytes.
	for i := 0; i < len(raw); i++ {
		chunks[i] = raw[i : i+1]
	}
	p := readFramedKeyboard(t, chunks...)
	if len(p.keys) != 1 || string(p.keys[0].Runes) != text {
		t.Fatalf("bytewise paste: %v", p.keys)
	}
}

func TestStandaloneEscapeAndAltKeys(t *testing.T) {
	for _, raw := range []string{"\x1b", "text\x1b", "\x1bx"} {
		source := inputChunks{raw}
		r := terminalInputReader{source: &source, escapeReady: func() (bool, error) { return false, nil }}
		p := make([]byte, 256)
		n, err := r.Read(p)
		if err != nil || string(p[:n]) != raw {
			t.Fatalf("%q: %q %v", raw, p[:n], err)
		}
	}
}

func TestOversizedCSIIsDiscarded(t *testing.T) {
	p := readFramedKeyboard(t, "\x1b["+strings.Repeat("1;", 600)+"R", "hello")
	if got := p.input.input.Value(); got != "hello" {
		t.Fatalf("oversized report leaked: %q", got)
	}
}

func TestLiteralSequenceTextAndLongInput(t *testing.T) {
	text := strings.Repeat("é[&358", 200) + " 👋"
	p := readFramedKeyboard(t, text)
	if got := p.input.input.Value(); got != text {
		t.Fatalf("ordinary input changed: %q", got)
	}
}

func TestPasteEndPrefixDoesNotBecomeEscape(t *testing.T) {
	source := inputChunks{"\x1b[200~text\x1b", "[201~"}
	r := terminalInputReader{source: &source, escapeReady: func() (bool, error) { t.Fatal("paste content triggered Escape timeout"); return false, nil }}
	buf := make([]byte, 256)
	n, err := r.Read(buf)
	if err != nil || string(buf[:n]) != "\x1b[200~text" {
		t.Fatalf("paste start: %q %v", buf[:n], err)
	}
	n, err = r.Read(buf)
	if err != nil || string(buf[:n]) != "\x1b[201~" {
		t.Fatalf("paste end: %q %v", buf[:n], err)
	}
}

func TestInterruptedReportDoesNotLeakOrSwallowQuit(t *testing.T) {
	for _, raw := range []string{"\x1b[35;", "\x1b[" + strings.Repeat("1;", 600)} {
		p := readFramedKeyboard(t, raw)
		if got := p.input.input.Value(); got != "" {
			t.Fatalf("interrupted report leaked %q", got)
		}
	}
	p := readFramedKeyboard(t, "\x1b[35;", "\x1b[13;2u", "hello")
	if got := p.input.input.Value(); got != "\nhello" {
		t.Fatalf("interrupted report swallowed next sequence: %q", got)
	}
}

func TestEnhancedKeyMarkersPreserveEventOrder(t *testing.T) {
	p := readFramedKeyboard(t, "\x1b[35;8R\x1b[97u", "\x00\x1b[13;2u", "\x1b[?1u\x1b[98u")
	if got := p.input.input.Value(); got != "a\nb" {
		t.Fatalf("key order changed: %q", got)
	}
	if len(p.keys) != 4 || p.keys[1].Type != tea.KeyCtrlAt {
		t.Fatalf("raw NUL or translated key changed: %v", p.keys)
	}
}
