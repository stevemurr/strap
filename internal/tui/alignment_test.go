package tui

import (
	"github.com/charmbracelet/x/ansi"
	"strings"
	"testing"
)

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
