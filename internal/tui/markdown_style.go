package tui

import (
	"strings"

	"github.com/charmbracelet/glamour"
	glamouransi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/muesli/termenv"
)

func newMarkdownRenderer(width int, dark bool, profile termenv.Profile) (*glamour.TermRenderer, error) {
	return glamour.NewTermRenderer(
		glamour.WithStyles(markdownStyle(width, dark, profile)),
		glamour.WithWordWrap(max(20, width)),
		glamour.WithColorProfile(profile),
	)
}

// A terminal has one font size: spacing, weight, and small level markers provide
// the heading hierarchy, including when color is disabled.
func markdownStyle(width int, dark bool, profile termenv.Profile) glamouransi.StyleConfig {
	style := styles.LightStyleConfig
	text, muted, accent, secondary, code, background := "235", "241", "#2563EB", "#006F87", "89", "254"
	theme := "github"
	if dark {
		style = styles.DarkStyleConfig
		text, muted, accent, secondary, code, background = "252", "247", "#60A5FA", "#22D3EE", "223", "236"
		theme = "monokai"
	}
	zero, one, two := uint(0), uint(1), uint(2)
	yes, no := true, false
	style.Document = glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Color: &text}, Margin: &zero}
	style.Heading = glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{BlockSuffix: "\n", Bold: &yes}}
	heading := func(prefix, color string, bold, italic, underline bool) glamouransi.StyleBlock {
		return glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{
			Prefix: prefix, Color: &color, Bold: &bold, Italic: &italic, Underline: &underline,
		}}
	}
	style.H1 = heading("▍ ", accent, true, false, true)
	style.H2 = heading("▎ ", accent, true, false, false)
	style.H3 = heading("▸ ", secondary, true, false, false)
	style.H4 = heading("› ", text, true, false, false)
	style.H5 = heading("· ", text, true, true, false)
	style.H6 = heading("· ", muted, false, true, false)
	style.Strong = glamouransi.StylePrimitive{Bold: &yes}
	style.Emph = glamouransi.StylePrimitive{Italic: &yes}
	style.Strikethrough = glamouransi.StylePrimitive{CrossedOut: &yes}
	style.BlockQuote = glamouransi.StyleBlock{
		StylePrimitive: glamouransi.StylePrimitive{Color: &muted},
		Indent:         &one, IndentToken: stringPointer("│ "),
	}
	style.List.LevelIndent = 2
	style.Item.BlockPrefix = "• "
	style.Task.Ticked, style.Task.Unticked = "[✓] ", "[ ] "
	style.HorizontalRule = glamouransi.StylePrimitive{
		Color: &muted, Format: "\n" + strings.Repeat("─", max(1, min(48, width))) + "\n",
	}
	style.Code = glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{
		Prefix: " ", Suffix: " ", Color: &code, BackgroundColor: &background,
	}}
	// Use named syntax themes rather than Glamour's global custom Chroma registry,
	// so light and dark renderers can coexist without sharing the first theme used.
	style.CodeBlock = glamouransi.StyleCodeBlock{
		StyleBlock: glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{
			Color: &text, BlockPrefix: "┌─ code\n", BlockSuffix: "└─\n",
		}, Indent: &two, Margin: &zero},
		Theme: theme,
	}
	if profile == termenv.Ascii {
		style.CodeBlock.Theme = "" // No syntax ANSI when NO_COLOR is set.
		style.Code.Prefix, style.Code.Suffix = "`", "`"
	}
	style.Link = glamouransi.StylePrimitive{Color: &secondary, Underline: &yes}
	style.LinkText = glamouransi.StylePrimitive{Color: &accent, Bold: &yes}
	style.ImageText = glamouransi.StylePrimitive{Color: &muted, Format: "Image: {{.text}} →"}
	style.Image = style.Link
	style.Table.CenterSeparator = stringPointer("┼")
	style.Table.ColumnSeparator = stringPointer("│")
	style.Table.RowSeparator = stringPointer("─")
	style.DefinitionTerm = glamouransi.StylePrimitive{Bold: &yes, Color: &text, BlockSuffix: "\n"}
	style.DefinitionDescription = glamouransi.StylePrimitive{BlockPrefix: "\n  ↳ ", Bold: &no}
	return style
}

func stringPointer(value string) *string { return &value }
