package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func TestMarkdownThemeFormatsNormalElements(t *testing.T) {
	source, err := os.ReadFile("testdata/markdown.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, dark := range []bool{false, true} {
		for _, profile := range []termenv.Profile{termenv.ANSI256, termenv.Ascii} {
			renderer, err := newMarkdownRenderer(80, dark, profile)
			if err != nil {
				t.Fatal(err)
			}
			rendered, err := renderer.Render(string(source))
			if err != nil {
				t.Fatal(err)
			}
			rendered = markdownText(rendered)
			plain := ansi.Strip(rendered)
			for _, want := range []string{
				"▍ Developer notes", "▎ Changes", "▸ Execution details", "› Code example", "· Results", "· References", "▎ Alternative heading",
				"• Agent attribution", "Nested items stay indented", "[✓] Markdown rendering", "[ ] Next iteration",
				"1. Read the configuration", "2. Apply the update", "│", "┌─ code", "└─", `return "Hello, " + name`,
				"Agent", "Operation", "Calls", "agent-2", "Write file", "https://example.com/docs", "Architecture", "↳ A short definition.",
			} {
				if !strings.Contains(plain, want) {
					t.Errorf("dark=%v profile=%v missing %q:\n%s", dark, profile, want, plain)
				}
			}
			for _, raw := range []string{"## Changes", "### Execution details", "**strong emphasis**", "*italics*", "~~old text~~", "```go", "| :---"} {
				if strings.Contains(plain, raw) {
					t.Errorf("unrendered markup %q", raw)
				}
			}
			if profile == termenv.Ascii && (strings.Contains(rendered, "38;") || strings.Contains(rendered, "48;")) {
				t.Fatal("color-disabled rendering emitted color codes")
			}
			if profile != termenv.Ascii && !strings.Contains(rendered, "\x1b[") {
				t.Fatal("theme emitted no styling")
			}
		}
	}
}

func TestMarkdownThemesRemainIndependent(t *testing.T) {
	source := "# Header\n\n```go\nreturn 42\n```\n"
	render := func(dark bool) string {
		renderer, err := newMarkdownRenderer(60, dark, termenv.ANSI256)
		if err != nil {
			t.Fatal(err)
		}
		output, err := renderer.Render(source)
		if err != nil {
			t.Fatal(err)
		}
		return markdownText(output)
	}
	dark, light := render(true), render(false)
	if dark == light {
		t.Fatal("light and dark themes are identical")
	}
	if dark != render(true) || light != render(false) {
		t.Fatal("rendering one theme mutated the other")
	}
}
