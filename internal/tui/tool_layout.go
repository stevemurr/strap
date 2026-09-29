package tui

import (
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// Cache the expensive wrapping of a result, including the hidden rows needed
// for the fold's line count. Replace the cache on resize/completion so frozen
// entry snapshots never have their layout mutated underneath them.
type toolOutputLayout struct {
	source             *toolDisplay
	width              int
	rows               []string
	detail             []string // the expanded view, when it differs from rows
	more               string   // what expanding shows, when rows do not say
	dark               bool
	profile            termenv.Profile
	preview, arguments string
}

func (e *entry) toolResultRows(width int) []string {
	dark, profile := lipgloss.HasDarkBackground(), lipgloss.ColorProfile()
	if c := e.toolLayout; c != nil && c.source == e.toolInfo && c.width == width && c.dark == dark && c.profile == profile {
		return c.rows
	}
	result := e.toolInfo.resultText()
	if result == "" {
		result = "(no output)"
		if e.toolInfo.finished.IsZero() {
			result = "Waiting for output…"
		}
	}
	rows := strings.Split(ansi.Hardwrap(result, width, true), "\n")
	var detail []string
	var more string
	if hits := e.toolInfo.hits; len(hits) > 0 {
		rows, detail = searchRows(hits, width), searchDetail(hits, width)
		if hidden := len(hits) - 2; hidden == 1 {
			more = "+1 result"
		} else if hidden > 1 {
			more = fmt.Sprintf("+%d results", hidden)
		}
	} else if p := e.toolInfo.page; p != nil {
		var text int
		rows = pageRows(p, width)
		if detail, text = pageDetail(p, width); text > 0 {
			more = fmt.Sprintf("+%d lines of page text", text)
		}
	}
	preview := routeStyle.Render(e.toolInfo.preview)
	if e.toolInfo.name == "shell" {
		preview = shellText(e.toolInfo.preview)
	} else if e.toolInfo.path != "" {
		preview = pathText(e.toolInfo.preview)
	}
	e.toolLayout = &toolOutputLayout{source: e.toolInfo, width: width, rows: rows, detail: detail, more: more, dark: dark, profile: profile, preview: preview, arguments: syntaxText(e.toolInfo.arguments, lexers.Get("json"))}
	return rows
}
