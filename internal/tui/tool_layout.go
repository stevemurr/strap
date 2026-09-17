package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Cache the expensive wrapping of a result, including the hidden rows needed
// for the fold's line count. Replace the cache on resize/completion so frozen
// entry snapshots never have their layout mutated underneath them.
type toolOutputLayout struct {
	source *toolDisplay
	width  int
	rows   []string
}

func (e *entry) toolResultRows(width int) []string {
	if c := e.toolLayout; c != nil && c.source == e.toolInfo && c.width == width {
		return c.rows
	}
	result := strings.TrimRight(e.toolInfo.result, "\n")
	if result == "" {
		result = "(no output)"
		if e.toolInfo.finished.IsZero() {
			result = "Waiting for output…"
		}
	}
	rows := strings.Split(ansi.Hardwrap(result, width, true), "\n")
	e.toolLayout = &toolOutputLayout{source: e.toolInfo, width: width, rows: rows}
	return rows
}
