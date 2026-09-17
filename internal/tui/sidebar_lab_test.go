package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestChipSurfaceClippingAndOcclusion(t *testing.T) {
	s := newChipSurface(10, 4)
	s.paint(-2, -1, "AAAAAAAA\nBBBBBBBB\nCCCCCCCC", 1)
	s.paint(3, 1, "front", 2)
	s.paint(5, 1, "x", -1)
	if s.hit(0, 0) != 1 || s.hit(3, 1) != 2 || s.hit(5, 1) != -1 || s.hit(10, 0) != -1 {
		t.Fatal("paint order and clipped input rectangles disagree")
	}
	if got := ansi.Strip(s.rows[1]); got != "CCCfrxnt  " {
		t.Fatalf("incorrect overlap: %q", got)
	}
	for _, row := range s.rows {
		if ansi.StringWidth(row) != 10 {
			t.Fatalf("canvas changed width: %q", row)
		}
	}
}

func TestSidebarLabHoverDoesNotSelectAndUsesStableTargets(t *testing.T) {
	m := newSidebarLab()
	// The exposed left edge of API remains its target when visually raised.
	m.Update(tea.MouseMsg{X: 3, Y: 17, Action: tea.MouseActionMotion})
	if m.hovered != 2 || m.selected != 0 {
		t.Fatal("hover should preview API without navigating")
	}
	if !strings.Contains(ansi.Strip(m.View()), "Running the route tests.") {
		t.Fatal("hover status is missing")
	}
	// Research owns this cell in the base layout, even after API is raised.
	m.Update(tea.MouseMsg{X: 16, Y: 17, Action: tea.MouseActionMotion})
	if m.hovered != 4 {
		t.Fatalf("raised chip captured a neighboring target: %d", m.hovered)
	}
	m.Update(tea.MouseMsg{X: 16, Y: 17, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m.selected != 4 {
		t.Fatal("click did not follow the previewed agent")
	}
	m.Update(tea.MouseMsg{X: 90, Y: 20, Action: tea.MouseActionMotion})
	if m.hovered != -1 || m.selected != 4 {
		t.Fatal("leaving sidebar should dismiss only the preview")
	}
}

func TestSidebarLabKeyboardCanInspectEveryAgent(t *testing.T) {
	m := newSidebarLab()
	for kind := range labNames {
		m.kind, m.focused = kind, -1
		for i := range labAgents {
			m.Update(tea.KeyMsg{Type: tea.KeyTab})
			if m.focused != i {
				t.Fatalf("concept %d skipped agent %d", kind, i)
			}
		}
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if m.selected != 7 || m.focused != -1 {
			t.Fatal("Enter should commit and dismiss preview")
		}
	}
}

func TestSidebarLabAllTargetsReachableAndFramesBounded(t *testing.T) {
	for kind := range labNames {
		s := labSidebar(kind, -1, 0, 33)
		seen := map[int]bool{}
		for y := 0; y < s.height; y++ {
			for x := 0; x < s.width; x++ {
				seen[s.hit(x, y)] = true
			}
		}
		for i := range labAgents {
			if !seen[i] {
				t.Fatalf("concept %d hides agent %d", kind, i)
			}
		}
		for _, size := range [][2]int{{112, 38}, {72, 36}, {80, 24}, {35, 12}, {1, 1}} {
			for focus := -1; focus < len(labAgents); focus++ {
				m := newSidebarLab()
				m.kind, m.focused, m.width, m.height = kind, focus, size[0], size[1]
				rows := strings.Split(m.View(), "\n")
				if len(rows) != size[1] {
					t.Fatal("frame height overflow")
				}
				for _, row := range rows {
					if ansi.StringWidth(row) != size[0] {
						t.Fatalf("frame width overflow: %q", row)
					}
				}
			}
		}
	}
}
