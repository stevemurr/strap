package tui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// renderBody caches immutable message bodies at the current viewport width.
// Spinner ticks and later events do not repeatedly parse earlier Markdown.
func (m *model) renderBody(e *entry) string {
	width := max(1, m.viewport.Width-1)
	if e.renderWidth == width {
		return e.rendered
	}
	body := e.body
	if e.agents != nil {
		body = e.agents.render(width)
	}
	if e.label == "Strap" || e.label == "Message" || e.label == "You" {
		if m.markdown == nil || m.markdownWidth != width {
			renderer, err := newMarkdownRenderer(width, lipgloss.HasDarkBackground(), lipgloss.ColorProfile())
			if err == nil {
				m.markdown, m.markdownWidth = renderer, width
			}
		}
		if m.markdown != nil {
			if rendered, err := m.markdown.Render(body); err == nil {
				body = strings.Trim(markdownText(rendered), "\n")
			}
		}
	}
	if e.reasoning != "" && e.reasoningExpanded {
		heading := "▾ Thinking · Ctrl+T hide\n" + e.reasoning
		// Keep the separator outside the styled block: Lip Gloss pads trailing
		// blank lines to the heading width, which would indent the answer.
		thinking := dimStyle.Render(heading)
		if body != "" {
			thinking += "\n\n"
		}
		body = thinking + body
	}
	// Wide tables and code lines must also fit after a terminal resize.
	wrapped := ansi.Hardwrap(lipgloss.NewStyle().AlignHorizontal(lipgloss.Left).Width(width).Render(body), width, true)
	var lines []string
	for line := range strings.SplitSeq(wrapped, "\n") {
		// A double-width glyph cannot fit in a one-column terminal.
		lines = append(lines, ansi.Truncate(line, width, ""))
	}
	e.rendered = strings.Join(lines, "\n")
	e.renderWidth = width
	return e.rendered
}

var sgrPattern = regexp.MustCompile("\x1b\\[[0-9;:]*m")

// Markdown entity decoding can introduce controls after input sanitization.
// Keep terminal text styles, but strip cursor, clipboard, and other commands.
func markdownText(rendered string) string {
	var out strings.Builder
	end := 0
	for _, match := range sgrPattern.FindAllStringIndex(rendered, -1) {
		out.WriteString(safeText(rendered[end:match[0]]))
		out.WriteString(rendered[match[0]:match[1]])
		end = match[1]
	}
	out.WriteString(safeText(rendered[end:]))
	return out.String()
}
