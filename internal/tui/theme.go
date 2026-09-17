package tui

import (
	"github.com/charmbracelet/lipgloss"
	"strings"
)

// Surfaces own both colors so custom terminal defaults cannot make their text
// disappear. Selection and the ready-to-send action share a contrasting pair.
var (
	surfaceTextColor  = lipgloss.AdaptiveColor{Light: "235", Dark: "252"}
	selectedTextColor = lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#0F172A"}
	selectedTextStyle = lipgloss.NewStyle().Foreground(selectedTextColor).Background(accentColor)
)

// Nested accent and cursor styles reset both foreground and background. Restore
// the enclosing surface after each reset, then reset once at its outer edge.
func renderSurface(style lipgloss.Style, text string) string {
	rendered := style.Render(text)
	base := lipgloss.NewStyle().Foreground(style.GetForeground()).Background(style.GetBackground())
	if colors := sgrPattern.FindString(base.Render(" ")); colors != "" {
		rendered = strings.ReplaceAll(rendered, "\x1b[0m", "\x1b[0m"+colors) + "\x1b[0m"
	}
	return rendered
}
