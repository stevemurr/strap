//go:build !darwin && !linux

package tui

import (
	"reflect"

	tea "github.com/charmbracelet/bubbletea"
)

// Without the framed reader, Bubble Tea's own input surfaces enhanced
// keyboard reports as unknown CSI messages; decode them here.
func composerInput() (tea.ProgramOption, func(), error) {
	return tea.WithFilter(terminalKeyFilter), func() {}, nil
}

// Bubble Tea v1 exposes unrecognized CSI input as an unexported []byte message.
// Only the fallback composerInput installs this bridge: the framed reader on
// darwin and linux decodes these reports itself. It leaves recognized keys,
// bracketed paste, and non-key CSI untouched. Remove it when Bubble Tea
// provides native enhanced-keyboard events.
func terminalKeyFilter(_ tea.Model, msg tea.Msg) tea.Msg {
	t := reflect.TypeOf(msg)
	if t == nil || t.PkgPath() != "github.com/charmbracelet/bubbletea" || t.Name() != "unknownCSISequenceMsg" {
		return msg
	}
	if key, ok := decodeTerminalKey(string(reflect.ValueOf(msg).Bytes())); ok {
		return key
	}
	return msg
}
