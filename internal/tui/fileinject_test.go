package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
)

func writeAttachmentFixture(t *testing.T, dir, path, content string) {
	t.Helper()
	path = filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
func initAttachmentRepo(t *testing.T, dir string) {
	t.Helper()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
}
func loadFixture(t *testing.T, dir, prompt string) attachmentResult {
	t.Helper()
	return loadAttachments(context.Background(), prompt, dir)
}

func TestParseFileRefs(t *testing.T) {
	for _, tc := range []struct {
		text string
		want []string
	}{
		{"@src/main.go and @src/main.go", []string{"src/main.go"}},
		{"check (@a.txt), then @b.go.", []string{"a.txt", "b.go"}},
		{"@\"a file.txt\" @'other file.go'", []string{"a file.txt", "other file.go"}},
		{"me@example.com \\@decorator @@literal @", nil},
		{"`@literal`\n```go\n@decorator\n```\n@real", []string{"real"}},
		{"@. @.. @../foo @/tmp/file", []string{".", "..", "../foo", "/tmp/file"}},
		{"@\"quote\\\"name\"", []string{"quote\"name"}},
	} {
		if got := parseFileRefs(tc.text); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: got %q want %q", tc.text, got, tc.want)
		}
	}
}

func TestAttachmentsPreservePromptAndDoNotExpandContent(t *testing.T) {
	dir := t.TempDir()
	writeAttachmentFixture(t, dir, "a.txt", "mentions @b.txt\n")
	writeAttachmentFixture(t, dir, "b.txt", "B\n")
	prompt := "check @a.txt and @b.txt, then @a.txt again"
	r := loadFixture(t, dir, prompt)
	if len(r.errs) > 0 || len(r.included) != 2 || !strings.HasPrefix(r.text, prompt+"\n\n<attached_files>") {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(r.text, "1\tmentions @b.txt\n") || strings.Count(r.text, "### @b.txt") != 1 {
		t.Fatal(r.text)
	}
}

func TestAttachmentsAbsoluteAndQuotedPaths(t *testing.T) {
	dir := t.TempDir()
	writeAttachmentFixture(t, dir, "a file.txt", "hello")
	for _, path := range []string{"a file.txt", filepath.Join(dir, "a file.txt")} {
		r := loadFixture(t, dir, quoteFileMention(path))
		if len(r.errs) > 0 || len(r.included) != 1 || !strings.Contains(r.text, "1\thello") {
			t.Fatalf("%+v", r)
		}
	}
}

func TestAttachmentsRejectUnsupportedAndOversizedFiles(t *testing.T) {
	for _, tc := range []struct{ name, content, want string }{
		{"data.bin", "a\x00b", "binary"}, {"encoded.txt", "\xff", "UTF-8"},
		{"terminal.txt", "\x1b[0m", "binary"}, {"fake.docx", "plain", "unsupported"},
		{"fake.xlsx", "plain", "unsupported"}, {"document.pdf", "%PDF-1.4", "unsupported"},
		{"renamed", "%PDF-1.4\n", "PDF"}, {"huge.txt", strings.Repeat("x", maxAttachmentFileBytes+1), "256 KiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeAttachmentFixture(t, dir, tc.name, tc.content)
			prompt := "@" + tc.name
			r := loadFixture(t, dir, prompt)
			if len(r.errs) != 1 || !strings.Contains(r.errs[0], tc.want) || r.text != prompt {
				t.Fatalf("%+v", r)
			}
		})
	}
}

func TestAttachmentsAcceptTextWithoutExtensionsAndExactFileLimit(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{"Dockerfile": "FROM scratch\n", "unicode": "你好 🌍\ttext\r\n", "empty": "", "limit": strings.Repeat("x", maxAttachmentFileBytes)} {
		writeAttachmentFixture(t, dir, name, data)
		r := loadFixture(t, dir, "@"+name)
		if len(r.errs) > 0 || len(r.included) != 1 {
			t.Fatalf("%s: %+v", name, r.errs)
		}
	}
}

func TestAttachmentsFolderUsesGitIgnoreAndReportsSkips(t *testing.T) {
	dir := t.TempDir()
	initAttachmentRepo(t, dir)
	for name, data := range map[string]string{
		".gitignore":   "build/\n*.log\n!keep.log\n/root-only.txt\n",
		"build/output": "secret", "debug.log": "secret", "keep.log": "keep",
		"root-only.txt": "secret", "nested/root-only.txt": "nested",
		"nested/.gitignore": "skip.txt\n", "nested/skip.txt": "secret",
		"nested/code.go": "package main", "asset": "\x00binary",
		"document.docx": "unsupported", "huge.txt": strings.Repeat("x", maxAttachmentFileBytes+1),
	} {
		writeAttachmentFixture(t, dir, name, data)
	}
	r := loadFixture(t, dir, "@.")
	if len(r.errs) > 0 || strings.Contains(r.text, "secret") || len(r.skipped) < 7 || !strings.Contains(r.text, "1\tkeep") || !strings.Contains(r.text, "1\tpackage main") || !strings.Contains(r.text, "1\tnested") {
		t.Fatalf("errs=%v included=%v skipped=%v payload=%s", r.errs, r.included, r.skipped, r.text)
	}
	r = loadFixture(t, dir, "@debug.log")
	if len(r.errs) != 1 || !strings.Contains(r.errs[0], "gitignored") {
		t.Fatalf("%+v", r)
	}
}

func TestAttachmentsBudgetFailureIsAtomic(t *testing.T) {
	dir := t.TempDir()
	for i := range 5 {
		writeAttachmentFixture(t, dir, fmt.Sprintf("%d.txt", i), strings.Repeat("a", maxAttachmentFileBytes))
	}
	r := loadFixture(t, dir, "@.")
	if len(r.errs) != 1 || !strings.Contains(r.errs[0], "1 MiB") || r.text != "@." {
		t.Fatalf("errs=%v", r.errs)
	}
}

func TestAttachmentsFormattedBudgetIsBounded(t *testing.T) {
	dir := t.TempDir()
	writeAttachmentFixture(t, dir, "lines", strings.Repeat("\n", maxAttachmentFileBytes))
	r := loadFixture(t, dir, "@lines")
	if len(r.errs) != 1 || !strings.Contains(r.errs[0], "1 MiB") {
		t.Fatalf("errs=%v", r.errs)
	}
}

func TestAttachmentsExplicitSkippedFileStillErrorsAfterFolder(t *testing.T) {
	dir := t.TempDir()
	writeAttachmentFixture(t, dir, "data/bin", "\x00")
	writeAttachmentFixture(t, dir, "data/text", "yes")
	r := loadFixture(t, dir, "@data @data/bin")
	if len(r.errs) != 1 || r.text != "@data @data/bin" {
		t.Fatalf("%+v", r)
	}
}

func TestAttachmentsCancelMissingEmptyAndMalformed(t *testing.T) {
	dir := t.TempDir()
	for _, prompt := range []string{"@missing", "@.", "@\"unclosed"} {
		r := loadFixture(t, dir, prompt)
		if len(r.errs) == 0 || r.text != prompt {
			t.Fatalf("%+v", r)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := loadAttachments(ctx, "@missing", dir); len(r.errs) != 1 || !strings.Contains(r.errs[0], "canceled") {
		t.Fatalf("%+v", r)
	}
}

func TestAttachmentsSkipSymlinksAndDedupeOverlaps(t *testing.T) {
	dir := t.TempDir()
	writeAttachmentFixture(t, dir, "folder/a", "A")
	if err := os.Symlink(dir, filepath.Join(dir, "folder/loop")); err != nil {
		t.Fatal(err)
	}
	r := loadFixture(t, dir, "@folder @folder/a")
	if len(r.errs) > 0 || len(r.included) != 1 || len(r.skipped) != 1 {
		t.Fatalf("%+v", r)
	}
}

// Run only the asynchronous composer commands, including commands batched by
// Update. No background event readers or spinner loops are needed in these tests.
func runFileCommands(t *testing.T, m *model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			runFileCommands(t, m, child)
		}
		return
	}
	switch msg.(type) {
	case cursor.BlinkMsg:
		return
	case attachmentsLoaded, fileCompletionsLoaded:
		_, next := m.Update(msg)
		runFileCommands(t, m, next)
	default:
		t.Fatalf("unexpected composer command %T", msg)
	}
}

func TestSubmitAttachmentsAsyncAndHistoryStaysRaw(t *testing.T) {
	t.Chdir(t.TempDir())
	writeAttachmentFixture(t, ".", "a.txt", "A")
	m, s := setup(t)
	m.input.SetValue("check @a.txt")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || len(s.sent) != 0 || m.attachmentJob == nil {
		t.Fatal("loading must be asynchronous")
	}
	_, duplicate := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if duplicate != nil {
		t.Fatal("duplicate submission")
	}
	runFileCommands(t, m, cmd)
	if len(s.sent) != 1 || !strings.Contains(s.sent[0], "1\tA") || m.history[0] != "check @a.txt" || m.input.Value() != "" {
		t.Fatalf("sent=%v history=%v", s.sent, m.history)
	}
	m.recall(-1)
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	runFileCommands(t, m, cmd)
	if len(s.sent) != 2 || s.sent[0] != s.sent[1] {
		t.Fatal("history replay changed attachment", s.sent)
	}
}

func TestSubmitAttachmentsPreservesDraftOnFailureEditOrCancel(t *testing.T) {
	t.Chdir(t.TempDir())
	writeAttachmentFixture(t, ".", "a.txt", "A")
	for _, mode := range []string{"missing", "edit", "cancel", "send error", "root stopped"} {
		t.Run(mode, func(t *testing.T) {
			m, s := setup(t)
			draft := "@a.txt"
			if mode == "missing" {
				draft = "@missing"
			}
			m.input.SetValue(draft)
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			switch mode {
			case "edit":
				m.input.SetValue("edited draft")
			case "cancel":
				m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			case "send error":
				s.err = fmt.Errorf("offline")
			case "root stopped":
				m.rootStopped = true
			}
			runFileCommands(t, m, cmd)
			if len(s.sent) != 0 || m.input.Value() == "" || len(m.history) != 0 {
				t.Fatalf("sent=%v draft=%s", s.sent, m.input.Value())
			}
		})
	}
}

func TestOrdinaryAtTextAndSlashCommandsDoNotLoadFiles(t *testing.T) {
	for _, draft := range []string{"email me@example.com", "literal `@decorator`", "literal @ and @@", "/help @missing"} {
		m, s := setup(t)
		m.input.SetValue(draft)
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd != nil || m.attachmentJob != nil {
			t.Fatal("unexpected loading", draft)
		}
		if !strings.HasPrefix(draft, "/") && (len(s.sent) != 1 || s.sent[0] != draft) {
			t.Fatal(s.sent)
		}
	}
}

func TestAttachmentEchoPreservesOriginalPromptInTranscript(t *testing.T) {
	t.Chdir(t.TempDir())
	writeAttachmentFixture(t, ".", "a.txt", "file contents")
	m, s := setup(t)
	m.input.SetValue("check @a.txt")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	runFileCommands(t, m, cmd)
	m.Update(received{event: conversation.MessageEvent{Message: message.Message{ID: "1", From: message.User, To: "root", Content: s.sent[0]}}})
	for _, entry := range m.entries {
		if entry.message == "1" && entry.body != "check @a.txt" {
			t.Fatal("echo replaced original prompt", entry.body)
		}
	}
}

func TestAttachmentsFileCountLimitIsAtomic(t *testing.T) {
	dir := t.TempDir()
	for i := range maxAttachmentFiles + 1 {
		writeAttachmentFixture(t, dir, fmt.Sprintf("file-%03d", i), "")
	}
	r := loadFixture(t, dir, "@.")
	if len(r.errs) != 1 || !strings.Contains(r.errs[0], "256 files") || r.text != "@." {
		t.Fatal(r.errs)
	}
}

func TestGitIgnoreHonorsNestedNegationAndRepositoryExcludes(t *testing.T) {
	dir := t.TempDir()
	initAttachmentRepo(t, dir)
	for name, data := range map[string]string{
		".gitignore":        "*.log\n/root-only\n",
		"nested/.gitignore": "!keep.log\n",
		"nested/keep.log":   "include this",
		"nested/drop.log":   "excluded content",
		".git/info/exclude": "local-secret\n",
		"local-secret":      "excluded content",
	} {
		writeAttachmentFixture(t, dir, name, data)
	}
	r := loadFixture(t, dir, "@.")
	if len(r.errs) > 0 || !strings.Contains(r.text, "include this") || strings.Contains(r.text, "excluded content") {
		t.Fatal(r.errs, r.text)
	}
}
