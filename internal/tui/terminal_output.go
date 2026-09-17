package tui

import (
	"bytes"
	"io"
	"os"
	"sync"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// Bubble Tea v1 flushes an alternate-screen frame in one Write beginning at
// cursor home, but a terminal can paint while parsing that write. Bracket the
// frame so terminals supporting synchronized output present it atomically.
// Preserve the file interface for Bubble Tea's terminal size/raw-mode handling;
// redirected output and test writers stay untouched.
func terminalOutput(output io.Writer) io.Writer {
	if output == nil {
		output = os.Stdout
	}
	if f, ok := output.(term.File); ok && term.IsTerminal(f.Fd()) {
		return &frameWriter{File: f}
	}
	return output
}

type frameWriter struct {
	term.File
	mu    sync.Mutex
	frame []byte
}

func (w *frameWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !bytes.HasPrefix(p, []byte(ansi.CursorHomePosition)) {
		return w.File.Write(p)
	}
	w.frame = append(w.frame[:0], ansi.SetSynchronizedOutputMode...)
	w.frame = append(w.frame, p...)
	w.frame = append(w.frame, ansi.ResetSynchronizedOutputMode...)
	n, err := w.File.Write(w.frame)
	if n < len(w.frame) {
		if err == nil {
			err = io.ErrShortWrite
		}
		// Do not leave drawing suspended after an incomplete terminal write.
		_, _ = io.WriteString(w.File, ansi.ResetSynchronizedOutputMode)
	}
	return min(len(p), max(0, n-len(ansi.SetSynchronizedOutputMode))), err
}
