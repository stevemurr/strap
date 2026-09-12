package tui

import (
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/x/ansi"
	"strings"
	"testing"
)

func TestAgentAnswerStartsAtLeftEdgeBelowThinking(t *testing.T) {
	for _, expanded := range []bool{false, true} {
		for _, width := range []int{80, 240} {
			m, _ := setup(t)
			m.viewport = viewport.New(width, 20)
			row := entry{label: "Strap", body: "Hey! I'm here and ready to help.", reasoning: "A short thought.", reasoningExpanded: expanded}
			rendered := ansi.Strip(m.renderBody(&row))
			found := false
			for _, line := range strings.Split(rendered, "\n") {
				if strings.Contains(line, "Hey!") {
					found = true
					if !strings.HasPrefix(line, "Hey!") {
						t.Errorf("expanded=%v width=%d: answer is indented: %q", expanded, width, line)
					}
				}
			}
			if !found {
				t.Fatalf("answer missing: %q", rendered)
			}
		}
	}
}

func TestUserMessageStartsAtLeftEdge(t *testing.T) {
	m, _ := setup(t)
	for _, text := range []string{"Hello there", "First paragraph\n\nSecond paragraph", "The user's response should be left aligned."} {
		enter(m, text)
		row := &m.entries[len(m.entries)-1]
		for _, line := range strings.Split(ansi.Strip(m.renderBody(row)), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			if strings.HasPrefix(line, " ") {
				t.Errorf("unexpected indentation: %q", line)
			}
		}
	}
}
