package tui

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

var (
	codeTextStyle = lipgloss.NewStyle().Foreground(surfaceTextColor)
	keywordStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#8551B4", Dark: "#C49BF0"})
	heredocStart  = regexp.MustCompile(`<<(-?)[ \t]*(?:'([A-Za-z_][A-Za-z_0-9]*)'|"([A-Za-z_][A-Za-z_0-9]*)"|([A-Za-z_][A-Za-z_0-9]*))`)
	redirectPath  = regexp.MustCompile(`(?:^|[^>])>>?[ \t]*(?:'([^']+)'|"([^"]+)"|([^\s<>;&|]+))`)
	numberedLine  = regexp.MustCompile(`^([0-9]+)(?:\t|:[a-z]+│)`)
)

// Style each physical line separately. Lipgloss pads a multiline string to its
// longest line; wrapping that padding creates spurious blank rows in source.
func styleLines(style lipgloss.Style, text string) string {
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = style.Render(line)
	}
	return strings.Join(lines, "\n")
}

func syntaxText(text string, lexer chroma.Lexer) string {
	text = strings.ReplaceAll(safeText(text), "\t", "    ")
	if lexer == nil || lipgloss.ColorProfile() == termenv.Ascii {
		return text
	}
	iterator, err := lexer.Tokenise(nil, text)
	if err != nil {
		return text
	}
	var out strings.Builder
	command := true
	shell := lexer.Config().Name == "Bash"
	for token := iterator(); token != chroma.EOF; token = iterator() {
		style := codeTextStyle
		switch {
		case token.Type.InCategory(chroma.Comment):
			style = dimStyle
		case token.Type == chroma.KeywordType || token.Type == chroma.NameClass:
			style = routeStyle
		case token.Type.InCategory(chroma.Keyword):
			style = keywordStyle
		case token.Type.InSubCategory(chroma.LiteralString):
			style = successStyle
		case token.Type.InSubCategory(chroma.LiteralNumber), token.Type.InCategory(chroma.Operator):
			style = stateStyle
		case token.Type == chroma.NameFunction || token.Type == chroma.NameBuiltin:
			style = accentStyle
		case token.Type == chroma.NameVariable:
			style = routeStyle
		}
		if shell {
			value := strings.TrimSpace(token.Value)
			if token.Type == chroma.Text && value != "" {
				switch {
				case strings.HasPrefix(value, "-"):
					style = keywordStyle
				case strings.Contains(value, "/") || strings.Contains(value, ".") && lexers.Match(value) != nil:
					style = routeStyle
				case command:
					style = accentStyle
				}
			}
			if value != "" {
				command = false
			}
			if strings.Contains(token.Value, "\n") || value == ";" || value == "&&" || value == "||" || value == "|" {
				command = true
			}
		}
		out.WriteString(styleLines(style, token.Value))
	}
	return out.String()
}

func pathText(path string) string {
	dir, name := filepath.Split(path)
	return dimStyle.Render(dir) + routeStyle.Render(name)
}

// Recognize simple file-writing heredocs for embedded source highlighting.
// This only selects a lexer: every source character and blank line is retained.
// Other shell syntax stays with the Bash lexer.
func shellText(command string) string {
	lines := strings.Split(command, "\n")
	bash := lexers.Get("bash")
	var out []string
	start := 0
	for i := 0; i < len(lines); i++ {
		matches := heredocStart.FindAllStringSubmatch(lines[i], -1)
		paths := redirectPath.FindAllStringSubmatch(lines[i], -1)
		if len(matches) != 1 || len(paths) != 1 {
			continue
		}
		match := matches[0]
		delimiter := match[2] + match[3] + match[4]
		lexer := lexers.Match(paths[0][1] + paths[0][2] + paths[0][3])
		if lexer == nil {
			continue
		}
		end := i + 1
		for end < len(lines) {
			line := lines[end]
			if match[1] == "-" {
				line = strings.TrimLeft(line, "\t")
			}
			if line == delimiter {
				break
			}
			end++
		}
		if end == len(lines) {
			continue
		}
		out = append(out, syntaxText(strings.Join(lines[start:i+1], "\n"), bash))
		if end > i+1 {
			out = append(out, syntaxText(strings.Join(lines[i+1:end], "\n"), lexer))
		}
		out = append(out, successStyle.Render(lines[end]))
		start, i = end+1, end
	}
	if start < len(lines) {
		out = append(out, syntaxText(strings.Join(lines[start:], "\n"), bash))
	}
	return strings.Join(out, "\n")
}

func (d *toolDisplay) resultText() string {
	text := strings.TrimRight(d.result, "\n")
	if d.name != "read_file" {
		if strings.HasPrefix(strings.TrimSpace(text), "{") || strings.HasPrefix(strings.TrimSpace(text), "[") {
			return syntaxText(text, lexers.Get("json"))
		}
		return styleLines(dimStyle, text)
	}
	lines := strings.Split(text, "\n")
	numbers := make([]string, len(lines))
	if d.numbered {
		for i, line := range lines {
			if match := numberedLine.FindStringSubmatch(line); match != nil {
				numbers[i], lines[i] = match[1], line[len(match[0]):]
			}
		}
	}
	lexer := lexers.Match(d.path)
	code := syntaxText(strings.Join(lines, "\n"), lexer)
	if lexer == nil {
		code = styleLines(dimStyle, code)
	}
	lines = strings.Split(code, "\n")
	for i := range lines {
		if i < len(numbers) && numbers[i] != "" {
			lines[i] = dimStyle.Render(numbers[i]+"    ") + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}
