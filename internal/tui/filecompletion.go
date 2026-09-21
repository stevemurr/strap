package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

type fileCandidate struct {
	path      string
	directory bool
}
type fileCompletionState struct {
	generation uint64
	exact      bool
	key        string
	cancel     context.CancelFunc
	candidates []fileCandidate
	err        string
}
type fileCompletionsLoaded struct {
	generation uint64
	exact      bool
	key        string
	candidates []fileCandidate
	err        error
}

func (m *model) inputOffset() int {
	lines := strings.Split(m.input.Value(), "\n")
	row := m.input.Line()
	offset := 0
	for i := 0; i < row; i++ {
		offset += len(lines[i]) + 1
	}
	col := m.input.LineInfo().StartColumn + m.input.LineInfo().ColumnOffset
	runes := []rune(lines[row])
	return offset + len(string(runes[:min(col, len(runes))]))
}

func (m *model) activeFileMention() (fileMention, bool) {
	if m.selecting || m.transcript != nil || m.plans.focused || m.streamUI.rosterFocused || m.completion.dismissed || m.attachmentJob != nil {
		return fileMention{}, false
	}
	text := m.input.Value()
	if strings.HasPrefix(strings.TrimSpace(text), "/") && !strings.Contains(text, "\n") {
		return fileMention{}, false
	}
	offset := m.inputOffset()
	// Don't complete in the middle of a token, where replacing its prefix would
	// leave the remainder behind. Editing earlier mentions at their end is fine.
	if offset < len(text) {
		r := []rune(text[offset:])[0]
		if !unicode.IsSpace(r) && !strings.ContainsRune(",;:!?)]}", r) {
			return fileMention{}, false
		}
	}
	refs := fileMentions(text[:offset])
	if len(refs) == 0 {
		return fileMention{}, false
	}
	ref := refs[len(refs)-1]
	return ref, ref.end == offset
}

func (m *model) fileCompletionKeyValue() string {
	ref, ok := m.activeFileMention()
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d:%s", ref.start, ref.end, m.input.Value())
}

func (m *model) scheduleFileCompletion() tea.Cmd {
	key := m.fileCompletionKeyValue()
	if m.quitting {
		key = ""
	}
	if key == m.fileCompletion.key {
		return nil
	}
	if m.fileCompletion.cancel != nil {
		m.fileCompletion.cancel()
	}
	generation := m.fileCompletion.generation + 1
	m.fileCompletion = fileCompletionState{key: key, generation: generation}
	if key == "" {
		return nil
	}
	ref, _ := m.activeFileMention()
	cwd, err := filepath.Abs(m.options.Dir)
	if err != nil {
		m.fileCompletion.err = err.Error()
		return nil
	}
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	m.fileCompletion.cancel = cancel
	return func() tea.Msg {
		defer cancel()
		timer := time.NewTimer(75 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return fileCompletionsLoaded{key: key, generation: generation, err: ctx.Err()}
		case <-timer.C:
		}
		candidates, err := findFileCandidates(ctx, cwd, ref.path)
		exact := false
		if ref.path != "" {
			path := ref.path
			if !filepath.IsAbs(path) {
				path = filepath.Join(cwd, path)
			}
			if info, statErr := os.Lstat(path); statErr == nil {
				exact = info.IsDir() || info.Mode().IsRegular()
			}
		}
		return fileCompletionsLoaded{key: key, generation: generation, exact: exact, candidates: candidates, err: err}
	}
}

func (m *model) finishFileCompletion(msg fileCompletionsLoaded) {
	if msg.generation != m.fileCompletion.generation || msg.key != m.fileCompletion.key || msg.key != m.fileCompletionKeyValue() {
		return
	}
	m.fileCompletion.cancel = nil
	m.fileCompletion.exact = msg.exact
	m.fileCompletion.candidates = msg.candidates
	if msg.err != nil {
		m.fileCompletion.err = msg.err.Error()
	}
}

func findFileCandidates(ctx context.Context, cwd, query string) ([]fileCandidate, error) {
	dir, prefix := filepath.Split(query)
	path := dir
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(maxAttachmentEntries + 1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > maxAttachmentEntries {
		return nil, fmt.Errorf("folder has over 10000 entries; type a narrower path")
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})
	var candidates []fileCandidate
	var paths []string
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.Name() == ".git" || !strings.HasPrefix(entry.Name(), prefix) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() > maxAttachmentFileBytes || unsupportedAttachment(entry.Name()) {
				continue
			}
		}
		name := dir + entry.Name()
		if entry.IsDir() {
			name += string(filepath.Separator)
		}
		candidates = append(candidates, fileCandidate{name, entry.IsDir()})
		paths = append(paths, filepath.Join(path, entry.Name()))
	}
	ignored, err := gitIgnored(ctx, path, paths)
	if err != nil {
		return nil, err
	}
	var visible []fileCandidate
	for i, candidate := range candidates {
		if !ignored[paths[i]] {
			visible = append(visible, candidate)
		}
	}
	return visible, nil
}

func quoteFileMention(path string) string {
	if strings.ContainsAny(path, " \t\r\n\"'`@<>()[]{}") || (path != "" && strings.ContainsAny(path[len(path)-1:], ".,;:!?")) {
		return "@\"" + strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(path) + "\""
	}
	return "@" + path
}

func (m *model) fileCompletionMatches() []slashCommand {
	if key := m.fileCompletionKeyValue(); key == "" || key != m.fileCompletion.key {
		return nil
	}
	var matches []slashCommand
	for _, c := range m.fileCompletion.candidates {
		description := "file · text only"
		if c.directory {
			description = "folder · Tab opens"
		}
		matches = append(matches, slashCommand{quoteFileMention(c.path), description})
	}
	return matches
}

func (m *model) completeFile(key string) bool {
	ref, ok := m.activeFileMention()
	if !ok {
		return false
	}
	if key == "esc" {
		m.completion.dismissed = true
		return true
	}
	if len(m.fileCompletionMatches()) == 0 {
		return false
	}
	switch key {
	case "up", "down":
		if m.completionHeight() == 0 {
			return false
		}
		delta := 1
		if key == "up" {
			delta = -1
		}
		m.completion.selected = (m.completion.selected + delta + len(m.fileCompletion.candidates)) % len(m.fileCompletion.candidates)
	case "enter", "tab":
		// Enter on an exact reference submits; Tab still navigates into a folder.
		if key == "enter" && ref.complete && ref.path != "" && m.fileCompletion.exact {
			return false
		}
		c := m.fileCompletion.candidates[m.completion.selected]
		replacement := quoteFileMention(c.path)
		if !c.directory && (ref.end == len(m.input.Value()) || !unicode.IsSpace([]rune(m.input.Value()[ref.end:])[0])) {
			replacement += " "
		}
		text := m.input.Value()
		m.input.SetValue(text[:ref.start] + replacement + text[ref.end:])
		m.setInputOffset(ref.start + len(replacement))
	default:
		return false
	}
	return true
}

func (m *model) setInputOffset(offset int) {
	before := m.input.Value()[:offset]
	lines := strings.Split(before, "\n")
	m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyCtrlHome})
	for range len(lines) - 1 {
		m.input.CursorEnd()
		m.input.CursorDown()
	}
	m.input.SetCursor(len([]rune(lines[len(lines)-1])))
}
