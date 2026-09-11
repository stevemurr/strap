package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

type screenPoint struct{ x, y int }

// A drag selects the screen the user actually saw. Events continue updating the
// underlying views; selection never moves beneath the mouse as output arrives.
type mouseSelection struct {
	lines      []string
	start, end screenPoint
	dragging   bool
	status     string
}

type clipboardResult struct {
	selection *mouseSelection
	err       error
}

func (s *mouseSelection) bounds() (screenPoint, screenPoint) {
	a, b := s.start, s.end
	if a.y > b.y || (a.y == b.y && a.x > b.x) {
		a, b = b, a
	}
	return a, b
}

// Expand partial wide characters to whole grapheme clusters so highlighting
// and copied text agree for CJK, combining marks, and emoji.
func selectionColumns(line string, left, right int) (int, int) {
	column := 0
	g := uniseg.NewGraphemes(ansi.Strip(line))
	for g.Next() {
		next := column + g.Width()
		if column < left && left < next {
			left = column
		}
		if column < right && right < next {
			right = next
		}
		column = next
	}
	return min(left, column), min(right, column)
}

func (s *mouseSelection) columns(row int) (int, int) {
	a, b := s.bounds()
	left, right := 0, ansi.StringWidth(s.lines[row])
	if row == a.y {
		left = a.x
	}
	if row == b.y {
		right = b.x + 1
	}
	return selectionColumns(s.lines[row], left, right)
}

func (s *mouseSelection) text() string {
	if s.start == s.end {
		return ""
	}
	a, b := s.bounds()
	var lines []string
	for row := a.y; row <= b.y; row++ {
		left, right := s.columns(row)
		text := ansi.Strip(ansi.Cut(s.lines[row], left, right))
		lines = append(lines, strings.TrimRight(text, " \t"))
	}
	return strings.Join(lines, "\n")
}

func (s *mouseSelection) view(width int) string {
	lines := append([]string(nil), s.lines...)
	a, b := s.bounds()
	if s.start != s.end {
		style := lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("12"))
		for row := a.y; row <= b.y; row++ {
			left, right := s.columns(row)
			line := lines[row]
			selected := ansi.Strip(ansi.Cut(line, left, right))
			lines[row] = ansi.Cut(line, 0, left) + style.Render(selected) + ansi.Cut(line, right, ansi.StringWidth(line))
		}
	}
	// Keep feedback outside the selected range so the visible text still matches
	// the copied snapshot when the footer itself is selected.
	if b.y < len(lines)-1 && s.status != "" {
		lines[len(lines)-1] = dimStyle.Render(ansi.Truncate(" "+s.status, width, "…"))
	}
	return strings.Join(lines, "\n")
}

func (m *model) selectWithMouse(event tea.MouseMsg) (bool, tea.Cmd) {
	if event.Button == tea.MouseButtonLeft && event.Action == tea.MouseActionPress {
		view := m.View()
		if m.mouseSelection != nil {
			view = strings.Join(m.mouseSelection.lines, "\n")
		}
		lines := strings.Split(view, "\n")
		point := screenPoint{max(0, min(event.X, m.width-1)), max(0, min(event.Y, len(lines)-1))}
		m.mouseSelection = &mouseSelection{lines: lines, start: point, end: point, dragging: true, status: "Drag to select · release to copy"}
		return true, nil
	}
	s := m.mouseSelection
	if s == nil || !s.dragging {
		return false, nil
	}
	if event.Action == tea.MouseActionMotion && event.Button == tea.MouseButtonLeft {
		s.end = screenPoint{max(0, min(event.X, m.width-1)), max(0, min(event.Y, len(s.lines)-1))}
		return true, nil
	}
	if event.Action == tea.MouseActionRelease && (event.Button == tea.MouseButtonLeft || event.Button == tea.MouseButtonNone) {
		s.end = screenPoint{max(0, min(event.X, m.width-1)), max(0, min(event.Y, len(s.lines)-1))}
		s.dragging = false
		if s.text() == "" {
			m.mouseSelection = nil
			return true, nil
		}
		return true, m.copySelection()
	}
	return false, nil
}

func (m *model) copySelection() tea.Cmd {
	s := m.mouseSelection
	text, write := s.text(), m.copyText
	s.status = "Copying…"
	return func() tea.Msg { return clipboardResult{selection: s, err: write(text)} }
}
