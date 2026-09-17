package tui

import (
	"fmt"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stevemurr/strap/message"
)

// Lipgloss uses a process-wide renderer. Restore it and keep these tests serial.
func withTerminalTheme(t *testing.T, dark bool, profile termenv.Profile) {
	t.Helper()
	oldDark, oldProfile := lipgloss.HasDarkBackground(), lipgloss.ColorProfile()
	lipgloss.SetHasDarkBackground(dark)
	lipgloss.SetColorProfile(profile)
	t.Cleanup(func() { lipgloss.SetHasDarkBackground(oldDark); lipgloss.SetColorProfile(oldProfile) })
}

func contrastRatio(a, b color.Color) float64 {
	luminance := func(c color.Color) float64 {
		r, g, b, _ := c.RGBA()
		linear := func(v uint32) float64 {
			x := float64(v) / 65535
			if x <= 0.04045 {
				return x / 12.92
			}
			return math.Pow((x+0.055)/1.055, 2.4)
		}
		return 0.2126*linear(r) + 0.7152*linear(g) + 0.0722*linear(b)
	}
	x, y := luminance(a), luminance(b)
	return (math.Max(x, y) + 0.05) / (math.Min(x, y) + 0.05)
}

func TestThemeSurfaceTextContrast(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, profile := range []termenv.Profile{termenv.TrueColor, termenv.ANSI256} {
			t.Run(fmt.Sprintf("dark=%v/profile=%v", dark, profile), func(t *testing.T) {
				withTerminalTheme(t, dark, profile)
				input := newInput()
				pairs := []struct {
					name   string
					fg, bg lipgloss.TerminalColor
				}{
					{"muted composer", dimStyle.GetForeground(), composerBackground},
					{"surface text", surfaceTextColor, composerBackground},
					{"selection and send", selectedTextColor, accentColor},
					{"focused placeholder", input.FocusedStyle.Placeholder.GetForeground(), composerBackground},
					{"blurred placeholder", input.BlurredStyle.Placeholder.GetForeground(), composerBackground},
					{"blurred draft", input.BlurredStyle.Text.GetForeground(), composerBackground},
				}
				background := lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#181818"}
				for name, style := range map[string]lipgloss.Style{"title": titleStyle, "command": routeStyle, "error": errorStyle, "state": stateStyle, "success": successStyle} {
					pairs = append(pairs, struct {
						name   string
						fg, bg lipgloss.TerminalColor
					}{name, style.GetForeground(), background})
				}
				for i := 0; i < 20; i++ {
					id := message.ActorID(fmt.Sprintf("agent-%d", i))
					pairs = append(pairs, struct {
						name   string
						fg, bg lipgloss.TerminalColor
					}{string(id), stackIdentity(id).GetForeground(), background})
				}
				markdown := markdownStyle(80, dark, profile)
				for name, color := range map[string]*string{"markdown link": markdown.Link.Color, "markdown heading": markdown.H1.Color} {
					pairs = append(pairs, struct {
						name   string
						fg, bg lipgloss.TerminalColor
					}{name, lipgloss.Color(*color), background})
				}
				for _, pair := range pairs {
					if ratio := contrastRatio(pair.fg, pair.bg); ratio < 4.5 {
						t.Errorf("%s contrast %.2f:1 is below 4.5:1", pair.name, ratio)
					}
				}
			})
		}
	}
}

func TestInterfaceThemeRendering(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, profile := range []termenv.Profile{termenv.TrueColor, termenv.ANSI256, termenv.Ascii} {
			t.Run(fmt.Sprintf("dark=%v/profile=%v", dark, profile), func(t *testing.T) {
				withTerminalTheme(t, dark, profile)
				m, _ := focusedSetup(t)
				m.resize(104, 40)
				shellTranscriptFixture(m)
				m.add("Strap", "## Progress\n\n**Readable text**, `inline code`, and [documentation](https://example.com).\n\n```go\nreturn true\n```", false)
				m.input.SetValue("Keep this draft\nwhile browsing agents")
				m.syncCompletion()
				m.working[m.session.Root()] = true
				states := map[string]string{"chat": m.View()}
				x, y := screenLocation(t, states["chat"], "Check the tests")
				selection := &mouseSelection{lines: strings.Split(states["chat"], "\n"), start: screenPoint{x, y}, end: screenPoint{x + 14, y}}
				states["selection"] = selection.view(m.width)
				m.input.Blur()
				states["blurred"] = m.View()
				m.input.SetValue("")
				states["placeholder"] = m.View()
				m.input.SetValue("Keep this draft")
				m.focusRoster(true)
				states["preview"] = m.View()
				e, _ := evalSetup(t)
				e.Update(tea.WindowSizeMsg{Width: 104, Height: 40})
				shellTranscriptFixture(e.current().activity)
				states["eval"] = e.View()
				for name, view := range states {
					if lipgloss.Height(view) > 40 || lipgloss.Width(view) > 104 {
						t.Fatalf("%s overflows the screen", name)
					}
					if profile == termenv.Ascii && regexp.MustCompile(`\x1b\[(?:[0-9]+;)*(?:3[0-8]|4[0-8]|9[0-7]|10[0-7])(?:;[0-9]+)*m`).MatchString(view) {
						t.Fatalf("%s emits styling with color disabled", name)
					}
					if profile != termenv.Ascii && !strings.Contains(view, "\x1b[") {
						t.Fatalf("%s did not exercise colored rendering", name)
					}
					// Optional actual ANSI frames for visual inspection; never golden snapshots.
					if dir := os.Getenv("STRAP_THEME_CAPTURE"); dir != "" {
						path := filepath.Join(dir, fmt.Sprintf("%t-%d-%s.ansi", dark, profile, name))
						if err := os.WriteFile(path, []byte(view), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
				for _, want := range []string{"Check the tests", "Progress", "Keep this draft", "Working"} {
					if !strings.Contains(ansi.Strip(states["chat"]), want) {
						t.Errorf("missing %q", want)
					}
				}
			})
		}
	}
}

// Read the effective background at a visible label, including nested SGR resets.
func backgroundAtLabel(t *testing.T, rendered, label string) string {
	t.Helper()
	end := strings.Index(rendered, label)
	if end < 0 {
		t.Fatalf("missing label %q", label)
	}
	background := "default"
	for _, seq := range sgrPattern.FindAllString(rendered[:end], -1) {
		parts := strings.Split(seq[2:len(seq)-1], ";")
		for i := 0; i < len(parts); i++ {
			code, _ := strconv.Atoi(parts[i])
			switch {
			case code == 0 || code == 49:
				background = "default"
			case code == 38 || code == 48:
				count := 2
				if i+1 < len(parts) && parts[i+1] == "2" {
					count = 4
				}
				if i+count >= len(parts) {
					t.Fatalf("invalid SGR %q", seq)
				}
				if code == 48 {
					background = strings.Join(parts[i+1:i+count+1], ";")
				}
				i += count
			case code >= 40 && code <= 47 || code >= 100 && code <= 107:
				background = parts[i]
			}
		}
	}
	return background
}

func TestThemeSurfacesKeepShadingAfterAccents(t *testing.T) {
	for _, dark := range []bool{false, true} {
		t.Run(fmt.Sprintf("dark=%t", dark), func(t *testing.T) {
			withTerminalTheme(t, dark, termenv.TrueColor)
			m, _ := focusedSetup(t)
			user := m.renderMessage(&entry{label: "You", body: "Check the tests"}, 0)
			expected := lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "#F4F4F4", Dark: "#262626"}).Render("padding")
			if backgroundAtLabel(t, user, "Check the tests") != backgroundAtLabel(t, expected, "padding") {
				t.Fatal("colored prompt removed message shading")
			}
			m.input.SetValue("draft text")
			field := strings.Join(m.composerView(), "\n")
			expected = lipgloss.NewStyle().Background(composerBackground).Render("padding")
			if backgroundAtLabel(t, field, "draft text") != backgroundAtLabel(t, expected, "padding") {
				t.Fatal("draft lost composer shading")
			}
			m.focusRoster(true)
			p := m.stackPeek()
			if p == nil {
				t.Fatal("missing preview")
			}
			expected = lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "255", Dark: "234"}).Render("padding")
			if backgroundAtLabel(t, p.text, "local-model") != backgroundAtLabel(t, expected, "padding") {
				t.Fatal("preview lost panel shading")
			}
		})
	}
}
