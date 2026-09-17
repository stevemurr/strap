package tui

// An isolated design playground: these fixtures never connect to a provider or
// change the production roster. The canvas and chip primitives use the same
// terminal cell geometry, Bubble Tea input, and Lip Gloss renderer as the TUI.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type labAgent struct {
	ID, Name, Short, Icon, Color, Role, State, Update, Task, Age string
	Unread                                                       int
}

var labAgents = []labAgent{
	{"root", "Coordinator", "Root", "◇", "#8BCDC8", "root", "working", "Bringing the API and renderer changes together.", "Make agent activity easier to follow", "now", 0},
	{"agent-2", "Schema", "Sch", "/", "#E8B477", "implementor", "attention", "Needs a decision: should unknown fields be rejected or preserved?", "Tighten the request schema", "18s ago", 2},
	{"agent-1", "API", "API", "/", "#92B9EE", "implementor", "working", "Running the route tests. The streaming endpoint now keeps its event order.", "Fix streaming event order", "3s ago", 3},
	{"agent-3", "Renderer", "TUI", "/", "#B3A2E8", "implementor", "working", "Checking wrapped lines at 80 and 120 columns.", "Keep the selected agent visible", "7s ago", 1},
	{"agent-4", "Research", "Res", "?", "#D7A0C4", "researcher", "working", "Comparing terminal mouse tracking modes and keyboard fallbacks.", "Explore compact agent navigation", "12s ago", 0},
	{"agent-5", "Audit", "Aud", "◎", "#A8C98E", "auditor", "working", "Reviewing the patch for focus changes and unread-count regressions.", "Review navigation behavior", "5s ago", 1},
	{"agent-6", "Docs", "Doc", "≡", "#9AAFC2", "implementor", "idle", "Draft is ready. Waiting for the interaction names to settle.", "Document navigation shortcuts", "1m ago", 0},
	{"agent-7", "Fixtures", "Fix", "✓", "#8FBBAA", "implementor", "completed", "Added eight deterministic fixtures. All checks passed.", "Add roster layout fixtures", "2m ago", 0},
}

var labNames = []string{"Stacks", "Signal rail", "Branch map"}

func labStyle(color string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color))
}

func labMark(state string) string {
	switch state {
	case "attention":
		return "!"
	case "working":
		return "●"
	case "completed":
		return "✓"
	default:
		return "○"
	}
}

func labChip(a labAgent, width int, active bool) string {
	style := labStyle(a.Color).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(a.Color))
	label := a.Icon + " " + a.Short + " " + labMark(a.State)
	if width >= 17 {
		label = a.Icon + " " + a.Name + " " + labMark(a.State)
		if a.Unread > 0 {
			label += fmt.Sprintf(" +%d", a.Unread)
		}
	}
	if active {
		style = style.Background(lipgloss.Color("#293440")).Bold(true)
	}
	return style.Render(fitStreamCell(label, width-2))
}

// Hover never rearranges targets: use the base surface for input even when a
// chip is lifted visually. Otherwise overlapping chips can oscillate on hover.
func labSidebar(kind, focused, selected, height int) *chipSurface {
	s := newChipSurface(rosterColumns, height)
	text := func(x, y int, value string) { s.paint(x, y, labStyle("#85929F").Render(value), -1) }
	text(1, 0, "CREW / 08")
	text(1, 1, "5 working · 1 needs you")
	rootStyle := labStyle(labAgents[0].Color)
	if focused == 0 {
		rootStyle = rootStyle.Background(lipgloss.Color("#293440")).Bold(true)
	}
	s.paint(1, 3, rootStyle.Render("◇ Coordinator              ●"), -1)
	s.hits = append(s.hits, chipHit{ID: 0, X: 1, Y: 3, Width: 31, Height: 1})
	if kind == 0 {
		text(1, 6, "! NEEDS YOU / 01")
		s.paint(1, 7, labChip(labAgents[1], 29, focused == 1), 1)
		text(1, 11, "● IN MOTION / 04")
		for i := 2; i <= 5; i++ {
			s.paint(1+(i-2)*7, 12, labChip(labAgents[i], 12, focused == i), i)
		}
		if focused >= 2 && focused <= 5 {
			s.paint(1+(focused-2)*7, 12, labChip(labAgents[focused], 12, true), focused)
		}
		text(1, 17, "○ AT REST / 01")
		s.paint(1, 18, labChip(labAgents[6], 17, focused == 6), 6)
		text(1, 23, "✓ LANDED / 01")
		s.paint(1, 24, labChip(labAgents[7], 17, focused == 7), 7)
	} else if kind == 1 {
		text(1, 6, "ACTIVITY SIGNALS")
		for i := 1; i < len(labAgents); i++ {
			a := labAgents[i]
			label := fmt.Sprintf("%s %-10s %s", a.Icon, a.Name, labMark(a.State))
			if a.Unread > 0 {
				label += fmt.Sprintf(" +%d", a.Unread)
			}
			style := labStyle(a.Color)
			if focused == i {
				style = style.Background(lipgloss.Color("#293440")).Bold(true)
			}
			s.paint(2, 8+(i-1)*2, style.Render(fitStreamCell(label, 28)), i)
		}
		text(2, 24, "! decision   ● working")
		text(2, 25, "○ idle       ✓ completed")
	} else {
		text(1, 6, "ONE TEAM / THREE BRANCHES")
		text(2, 8, "┌─ Build ───────────────────┐")
		text(2, 9, "│                          │")
		text(2, 13, "├─ Discover & review ───────┤")
		text(2, 14, "│                          │")
		text(2, 18, "└─ Wrap up ────────────────┘")
		positions := [][3]int{{1, 1, 10}, {2, 12, 10}, {3, 23, 10}, {4, 5, 15}, {5, 19, 15}, {6, 5, 20}, {7, 19, 20}}
		for _, p := range positions {
			s.paint(p[1], p[2], labChip(labAgents[p[0]], 10, focused == p[0]), p[0])
		}
		text(1, 25, "! Schema needs a decision")
	}
	if selected >= 0 && selected < len(labAgents) {
		text(1, 29, "VIEWING / "+labAgents[selected].Name)
	}
	text(1, 31, "Hover to peek · click to follow")
	return s
}

type sidebarLab struct {
	width, height, kind, selected, focused, hovered int
}

func newSidebarLab() *sidebarLab {
	return &sidebarLab{width: 112, height: 38, hovered: -1, focused: -1}
}

func (m *sidebarLab) Init() tea.Cmd { return nil }

func (m *sidebarLab) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "1", "2", "3":
			m.kind = int(msg.String()[0] - '1')
			m.hovered = -1
		case "tab", "down", "j":
			m.focused = (max(-1, m.focused) + 1) % len(labAgents)
			m.hovered = -1
		case "shift+tab", "up", "k":
			m.focused = (max(0, m.focused) + len(labAgents) - 1) % len(labAgents)
			m.hovered = -1
		case "enter", " ":
			if m.focused >= 0 {
				m.selected = m.focused
			}
			m.focused = -1
		case "esc":
			m.focused, m.hovered = -1, -1
		}
	case tea.MouseMsg:
		if m.width < 72 || m.height < 36 {
			return m, nil
		}
		if msg.Y == 2 && msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			for i, x := range []int{1, 15, 33} {
				if msg.X >= x && msg.X < x+len(labNames[i])+4 {
					m.kind = i
					m.hovered = -1
					return m, nil
				}
			}
		}
		id := labSidebar(m.kind, -1, m.selected, m.height-5).hit(msg.X, msg.Y-4)
		if msg.Action == tea.MouseActionMotion {
			m.hovered, m.focused = id, -1
		}
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft && id >= 0 {
			m.selected, m.hovered, m.focused = id, -1, -1
		}
	}
	return m, nil
}

func (m *sidebarLab) View() string {
	s := newChipSurface(m.width, m.height)
	if m.width < 72 || m.height < 36 {
		s.paint(0, 0, "Sidebar lab · use 72×36 or larger\n1–3 concepts · q quit", -1)
		return s.String()
	}
	s.paint(1, 0, labStyle("#D6DFE8").Bold(true).Render("strap / sidebar lab")+labStyle("#85929F").Render("   ·   fixture data / no live agents"), -1)
	for i, x := range []int{1, 15, 33} {
		style := labStyle("#85929F")
		if m.kind == i {
			style = labStyle("#8BCDC8").Bold(true)
		}
		s.paint(x, 2, style.Render(fmt.Sprintf("%d %s", i+1, labNames[i])), -1)
	}
	peek := m.focused
	if m.hovered >= 0 {
		peek = m.hovered
	}
	s.paint(0, 4, labSidebar(m.kind, peek, m.selected, m.height-5).String(), -1)
	for y := 4; y < m.height-1; y++ {
		s.paint(35, y, labStyle("#3B4652").Render("│"), -1)
	}
	a := labAgents[m.selected]
	w := m.width - 40
	s.paint(39, 4, labStyle(a.Color).Bold(true).Render(a.Icon+" "+a.Name+" / "+a.State), -1)
	s.paint(39, 6, labStyle("#85929F").Render("following "+a.ID+" · "+a.Role), -1)
	s.paint(39, 9, labStyle("#D6DFE8").Render(lipgloss.NewStyle().Width(w).Render(a.Task)), -1)
	s.paint(39, 12, labStyle("#A7B3BF").Render(lipgloss.NewStyle().Width(w).Render(a.Update)), -1)
	s.paint(39, 18, labStyle("#85929F").Render("Peek at a teammate while keeping\nyour place in this conversation."), -1)
	s.paint(39, m.height-5, labStyle("#85929F").Render(strings.Repeat("─", w)), -1)
	s.paint(39, m.height-3, labStyle("#A7B3BF").Render("› Message root…"), -1)
	if peek >= 0 {
		a = labAgents[peek]
		content := labStyle(a.Color).Bold(true).Render(a.Icon+" "+a.Name+"  "+labMark(a.State)+" "+a.State) + "\n" +
			labStyle("#85929F").Render(a.ID+" · "+a.Role+" · "+a.Age) + "\n\n" +
			lipgloss.NewStyle().Width(w-4).Render(a.Update) + "\n\n" +
			labStyle("#85929F").Render(fmt.Sprintf("%d unread · Enter / click to follow", a.Unread))
		panel := lipgloss.NewStyle().Foreground(lipgloss.Color("#D6DFE8")).Background(lipgloss.Color("#1E2833")).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(a.Color)).Padding(1).Width(w).Render(content)
		s.paint(37, 8, panel, -1)
	}
	s.paint(1, m.height-1, labStyle("#85929F").Render("1–3 concept   ↑↓ / Tab peek   Enter follow   Esc dismiss   q quit"), -1)
	return s.String()
}

// RunSidebarLab opens an offline, mouse- and keyboard-operable mockup.
func RunSidebarLab() error {
	_, err := tea.NewProgram(newSidebarLab(), tea.WithAltScreen(), tea.WithMouseAllMotion()).Run()
	return err
}

// SidebarLabSnapshot renders a deterministic full terminal frame for review.
func SidebarLabSnapshot(kind, focused int) string {
	m := newSidebarLab()
	m.kind, m.focused = max(0, min(2, kind)), max(-1, min(len(labAgents)-1, focused))
	return m.View()
}

// SidebarLabPreview carries actual Lip Gloss renders into the browser mockup.
// Hit rectangles are from the unraised layout, matching terminal behavior.
func SidebarLabPreview() any {
	type variant struct {
		Name    string
		Base    string
		Focused []string
		Hits    []chipHit
	}
	var variants []variant
	for kind, name := range labNames {
		base := labSidebar(kind, -1, 0, 33)
		v := variant{Name: name, Base: base.String(), Hits: base.hits}
		for i := range labAgents {
			v.Focused = append(v.Focused, labSidebar(kind, i, 0, 33).String())
		}
		variants = append(variants, v)
	}
	return struct {
		Agents   []labAgent
		Variants []variant
	}{labAgents, variants}
}
