package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type attachmentJob struct {
	draft  string
	cancel context.CancelFunc
}
type attachmentsLoaded struct {
	job    *attachmentJob
	result attachmentResult
}

func (m *model) startAttachments(draft string) tea.Cmd {
	cwd, err := filepath.Abs(m.options.Dir)
	if err != nil {
		m.add("Error", err.Error(), true)
		return nil
	}
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	job := &attachmentJob{draft: draft, cancel: cancel}
	m.attachmentJob = job
	m.add("Files", "Loading text attachments… Esc cancels; editing the draft prevents sending.", true)
	return func() tea.Msg {
		defer cancel()
		return attachmentsLoaded{job, loadAttachments(ctx, draft, cwd)}
	}
}

func (m *model) finishAttachments(msg attachmentsLoaded) {
	if m.attachmentJob != msg.job {
		return
	}
	m.attachmentJob = nil
	defer m.refreshActivity()
	if m.input.Value() != msg.job.draft {
		m.add("Files", "Draft changed while loading attachments. Send again to load the updated references.", true)
		return
	}
	r := msg.result
	if len(r.skipped) > 0 {
		m.add("Files", attachmentList("Skipped", r.skipped), true)
	}
	if len(r.errs) > 0 {
		m.add("Error", strings.Join(r.errs, "\n"), true)
		return
	}
	if m.sendDraft(msg.job.draft, r.text) && len(r.included) > 0 {
		m.add("Files", fmt.Sprintf("Attached %d text file(s), %d bytes.\n", len(r.included), r.bytes)+attachmentList("Included", r.included), true)
	}
}

func attachmentList(label string, paths []string) string {
	// Keep summaries bounded for very large folders; report the full count.
	n := min(len(paths), 20)
	text := fmt.Sprintf("%s (%d):\n%s", label, len(paths), strings.Join(paths[:n], "\n"))
	if n < len(paths) {
		text += fmt.Sprintf("\n… and %d more", len(paths)-n)
	}
	return text
}
