//go:build !darwin && !linux

package tui

import tea "github.com/charmbracelet/bubbletea"

func composerInput() (tea.ProgramOption, func(), error) {
	return func(*tea.Program) {}, func() {}, nil
}
