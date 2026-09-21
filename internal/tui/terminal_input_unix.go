//go:build darwin || linux

package tui

import (
	"errors"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"
)

// Preserve File's descriptor and name so Bubble Tea still owns raw mode,
// cancellable reads, suspension, and restoration.
type framedTerminalInput struct {
	*os.File
	reader terminalInputReader
}

func (f *framedTerminalInput) Read(p []byte) (int, error) { return f.reader.Read(p) }

func composerInput() (tea.ProgramOption, func(), error) {
	file := os.Stdin
	closeInput := func() {}
	if !term.IsTerminal(file.Fd()) {
		var err error
		file, err = os.Open("/dev/tty")
		if err != nil {
			return nil, closeInput, err
		}
		closeInput = func() { _ = file.Close() }
	}
	input := newFramedTerminalInput(file)
	return func(p *tea.Program) {
		tea.WithInput(input)(p)
		tea.WithFilter(input.reader.filter)(p)
	}, closeInput, nil
}

func newFramedTerminalInput(file *os.File) *framedTerminalInput {
	input := &framedTerminalInput{File: file}
	input.reader = terminalInputReader{source: file, escapeReady: func() (bool, error) {
		fds := []unix.PollFd{{Fd: int32(file.Fd()), Events: unix.POLLIN}}
		for {
			n, err := unix.Poll(fds, 25)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return n > 0, err
		}
	}}
	return input
}
