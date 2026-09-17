package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// chipSurface composites opaque, cell-aligned layers. Painting and hit testing
// share the same clipped rectangles, with the last painted layer on top.
// This is deliberately independent of sessions and agent lifecycle state.
type chipSurface struct {
	width, height int
	rows          []string
	hits          []chipHit
}

type chipHit struct {
	ID                  int
	X, Y, Width, Height int
}

func newChipSurface(width, height int) *chipSurface {
	s := &chipSurface{width: max(0, width), height: max(0, height)}
	for range s.height {
		s.rows = append(s.rows, strings.Repeat(" ", s.width))
	}
	return s
}

func (s *chipSurface) paint(x, y int, content string, id int) {
	lines := strings.Split(content, "\n")
	w := lipgloss.Width(content)
	x0, x1 := max(0, x), min(s.width, x+w)
	y0, y1 := max(0, y), min(s.height, y+len(lines))
	if x0 >= x1 || y0 >= y1 {
		return
	}
	for row := y0; row < y1; row++ {
		line := fitStreamCell(lines[row-y], w)
		s.rows[row] = ansi.Cut(s.rows[row], 0, x0) + ansi.Cut(line, x0-x, x1-x) + ansi.Cut(s.rows[row], x1, s.width)
	}
	// A decorative layer also occludes underlying targets.
	s.hits = append(s.hits, chipHit{ID: id, X: x0, Y: y0, Width: x1 - x0, Height: y1 - y0})
}

func (s *chipSurface) hit(x, y int) int {
	for i := len(s.hits) - 1; i >= 0; i-- {
		h := s.hits[i]
		if x >= h.X && x < h.X+h.Width && y >= h.Y && y < h.Y+h.Height {
			return h.ID
		}
	}
	return -1
}

func (s *chipSurface) String() string { return strings.Join(s.rows, "\n") }
