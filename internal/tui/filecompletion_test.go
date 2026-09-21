package tui

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func typeFileQuery(t *testing.T, m *model, text string) {
	t.Helper()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
	runFileCommands(t, m, cmd)
}

func TestFilePickerNavigatesFolderAndQuotesSpaces(t *testing.T) {
	t.Chdir(t.TempDir())
	writeAttachmentFixture(t, ".", "src/a file.go", "package main")
	m, s := setup(t)
	typeFileQuery(t, m, "look @")
	if got := m.completionMatches(); len(got) != 1 || got[0].name != "@src/" {
		t.Fatal(got)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	runFileCommands(t, m, cmd)
	if m.input.Value() != "look @src/" || len(m.completionMatches()) != 1 {
		t.Fatalf("draft=%q matches=%v", m.input.Value(), m.completionMatches())
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	runFileCommands(t, m, cmd)
	if m.input.Value() != `look @"src/a file.go" ` || len(s.sent) != 0 {
		t.Fatal(m.input.Value(), s.sent)
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	runFileCommands(t, m, cmd)
	if len(s.sent) != 1 || !strings.Contains(s.sent[0], "1\tpackage main") {
		t.Fatal(s.sent)
	}
}

func TestFilePickerExactReferenceSendsAndPartialCompletes(t *testing.T) {
	t.Chdir(t.TempDir())
	writeAttachmentFixture(t, ".", "alpha.go", "A")
	for _, query := range []string{"@alpha.go", "@al"} {
		m, s := setup(t)
		typeFileQuery(t, m, query)
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		runFileCommands(t, m, cmd)
		if query == "@al" {
			if len(s.sent) != 0 || m.input.Value() != "@alpha.go " {
				t.Fatal(s.sent, m.input.Value())
			}
			_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			runFileCommands(t, m, cmd)
		}
		if len(s.sent) != 1 {
			t.Fatal(s.sent)
		}
	}
}

func TestFilePickerCompletesEarlierMultilineMentionWithoutLosingSuffix(t *testing.T) {
	t.Chdir(t.TempDir())
	writeAttachmentFixture(t, ".", "alpha.go", "A")
	m, _ := setup(t)
	m.input.SetValue("前言 🌍\nlook @al then explain\nlast line")
	m.setInputOffset(len("前言 🌍\nlook @al"))
	_, cmd := m.Update(nil)
	runFileCommands(t, m, cmd)
	if len(m.completionMatches()) != 1 {
		t.Fatal(m.completionMatches())
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	runFileCommands(t, m, cmd)
	if m.input.Value() != "前言 🌍\nlook @alpha.go then explain\nlast line" || m.inputOffset() != len("前言 🌍\nlook @alpha.go") {
		t.Fatal(m.input.Value(), m.inputOffset())
	}
}

func TestFilePickerFiltersIgnoredUnsupportedLargeAndSymlinkEntries(t *testing.T) {
	dir := t.TempDir()
	initAttachmentRepo(t, dir)
	for name, data := range map[string]string{".gitignore": "build/\n*.log\n!keep.log\n", "build/x": "hidden", "debug.log": "hidden", "keep.log": "yes", "a.pdf": "pdf", "big": strings.Repeat("x", maxAttachmentFileBytes+1), "Dockerfile": "FROM scratch"} {
		writeAttachmentFixture(t, dir, name, data)
	}
	if err := os.Symlink(filepath.Join(dir, "Dockerfile"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	got, err := findFileCandidates(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range got {
		names = append(names, c.path)
	}
	if !reflect.DeepEqual(names, []string{".gitignore", "Dockerfile", "keep.log"}) {
		t.Fatal(names)
	}
}

func TestFilePickerStaleResultsAndEscape(t *testing.T) {
	t.Chdir(t.TempDir())
	writeAttachmentFixture(t, ".", "alpha.go", "A")
	writeAttachmentFixture(t, ".", "beta.go", "B")
	m, _ := setup(t)
	_, oldCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("@a")})
	oldKey := m.fileCompletion.key
	m.input.SetValue("@b")
	_, cmd := m.Update(nil)
	runFileCommands(t, m, cmd)
	runFileCommands(t, m, oldCmd)
	m.Update(fileCompletionsLoaded{key: oldKey, candidates: []fileCandidate{{"wrong", false}}})
	if got := m.completionMatches(); len(got) != 1 || got[0].name != "@beta.go" {
		t.Fatal(got)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if len(m.completionMatches()) != 0 || m.input.Value() != "@b" {
		t.Fatal("escape lost draft")
	}
	m.Update(nil)
	if len(m.completionMatches()) != 0 {
		t.Fatal("menu reopened")
	}
	typeFileQuery(t, m, "e")
	if len(m.completionMatches()) != 1 {
		t.Fatal("editing didn't reopen picker")
	}
}

func TestFilePickerIgnoresEmailsCodeAndMiddleOfToken(t *testing.T) {
	for _, draft := range []string{"me@example.com", "`@literal`", "\\@escaped", "/inspect @root"} {
		m, _ := setup(t)
		m.input.SetValue(draft)
		if _, ok := m.activeFileMention(); ok {
			t.Fatal(draft)
		}
	}
	m, _ := setup(t)
	m.input.SetValue("@alpha.go")
	m.setInputOffset(3)
	if _, ok := m.activeFileMention(); ok {
		t.Fatal("completion in middle of path")
	}
}

func TestFilePickerNavigationAndSmallTerminalBounds(t *testing.T) {
	t.Chdir(t.TempDir())
	writeAttachmentFixture(t, ".", "alpha.go", "A")
	writeAttachmentFixture(t, ".", "beta.go", "B")
	m, _ := setup(t)
	typeFileQuery(t, m, "@")
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.completion.selected != 1 {
		t.Fatal("selection didn't wrap")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.completion.selected != 0 {
		t.Fatal("selection didn't wrap")
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 80, Height: 24}, {Width: 25, Height: 12}, {Width: 1, Height: 1}} {
		m.Update(size)
		lines := strings.Split(m.View(), "\n")
		if len(lines) > size.Height {
			t.Fatal("picker exceeds terminal height")
		}
		for _, line := range lines {
			if lipgloss.Width(line) > size.Width {
				t.Fatal("picker exceeds terminal width")
			}
		}
	}
}

func TestFilePickerEnterSendsExactFolderAfterNavigation(t *testing.T) {
	t.Chdir(t.TempDir())
	writeAttachmentFixture(t, ".", "src/a.go", "A")
	m, s := setup(t)
	typeFileQuery(t, m, "@sr")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	runFileCommands(t, m, cmd)
	if m.input.Value() != "@src/" {
		t.Fatal(m.input.Value())
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	runFileCommands(t, m, cmd)
	if len(s.sent) != 1 || !strings.HasPrefix(s.sent[0], "@src/\n") {
		t.Fatal(s.sent, m.input.Value())
	}
}

func TestFilePickerEscapeWhileSearchPendingAndRepeatedQuery(t *testing.T) {
	t.Chdir(t.TempDir())
	writeAttachmentFixture(t, ".", "a.go", "A")
	m, _ := setup(t)
	_, oldCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("@a")})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !m.completion.dismissed {
		t.Fatal("Escape didn't dismiss pending search")
	}
	m.input.SetValue("other")
	m.Update(nil)
	m.input.SetValue("@a")
	_, cmd := m.Update(nil)
	runFileCommands(t, m, cmd)
	runFileCommands(t, m, oldCmd)
	if len(m.completionMatches()) != 1 || m.fileCompletion.err != "" {
		t.Fatal("stale canceled search replaced fresh result")
	}
}

func TestFilePickerAndAttachmentsUseConfiguredWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	writeAttachmentFixture(t, dir, "unique.go", "configured workspace")
	m, s := setup(t)
	m.options.Dir = dir
	typeFileQuery(t, m, "@uniq")
	if len(m.completionMatches()) != 1 {
		t.Fatal(m.completionMatches())
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	runFileCommands(t, m, cmd)
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	runFileCommands(t, m, cmd)
	if len(s.sent) != 1 || !strings.Contains(s.sent[0], "configured workspace") {
		t.Fatal(s.sent)
	}
}
