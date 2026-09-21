package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
)

func (m *model) addProgress(event work.Event) {
	e := entry{label: "Progress", meta: string(event.Work.Assignee), actors: []message.ActorID{event.Work.Assignee}, body: progressSummary(event), reportDetail: &entry{label: "Progress", body: safeText(progressBody(event))}}
	if event.Change != nil {
		for _, r := range event.Change.ProgressReports {
			e.reportRefs = append(e.reportRefs, message.ProgressReportRef{WorkID: r.WorkID, ReportID: r.ID, WorkRevision: r.WorkRevision, AssignedAtRevision: r.AssignedAtRevision})
		}
	}
	m.addEntry(e, false)
}

func (m *model) hasProgressReports(notice *message.WorkProgressNotice) bool {
	if len(notice.Reports) == 0 || len(notice.Briefs) > 0 || len(notice.Covered) > 0 || notice.Attention {
		return false
	}
	for _, ref := range notice.Reports {
		found := false
		for _, e := range m.entries {
			if slices.Contains(e.reportRefs, ref) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Labels live in their own paragraphs. Even a single-line value may be a
// Markdown block (a heading, list item, quote, or indented code); prefixing it
// with a label changes how Markdown parses it.
func progressField(label, value string) string {
	return "**" + label + ":**\n\n" + value
}

func progressSummaryField(label, value string) string {
	// Block values keep their own paragraph so headings, lists, and code retain
	// their Markdown meaning. Short narrative fields fit on one terminal row.
	if value == "" || strings.Contains(value, "\n") || strings.HasPrefix(value, "    ") || strings.HasPrefix(value, "\t") || strings.ContainsAny(value[:1], "#>*-+`~0123456789") {
		return progressField(label, value)
	}
	return "**" + label + ":** " + value
}

func progressBody(e work.Event) string {
	var lines []string
	if e.Change == nil {
		return e.Work.Task
	}
	for _, r := range e.Change.ProgressReports {
		lines = append(lines, fmt.Sprintf("Report %s · work %s · revision %d · assignment %d", r.ID, r.WorkID, r.WorkRevision, r.AssignedAtRevision))
		if p := r.Position; p != nil {
			lines = append(lines, progressField("Objective", p.Objective))
			for _, field := range [][2]string{{"Activity", p.Activity}, {"Note", p.Note}, {"Next", p.NextStep}, {"Uncertainty", p.Uncertainty}, {"Blocker", p.Blocker}, {"Decision needed", p.DecisionNeed}} {
				if field[1] != "" {
					lines = append(lines, progressField(field[0], field[1]))
				}
			}
		}
		for _, f := range r.Findings {
			lines = append(lines, progressField(string(f.Basis)+" · "+string(f.ID), f.Claim))
			if f.Limitation != "" {
				lines = append(lines, progressField("Limitation", f.Limitation))
			}
			for _, e := range f.Evidence {
				lines = append(lines, progressField("Source", e.URI))
			}
		}
	}
	for _, b := range e.Change.ResearchBriefs {
		lines = append(lines, "Research delivered · "+string(b.ID), b.Summary)
		if b.Recommendation != "" {
			lines = append(lines, progressField("Recommendation", b.Recommendation))
		}
		for _, q := range b.OpenQuestions {
			lines = append(lines, progressField("Open", q))
		}
	}
	return strings.Join(lines, "\n\n")
}
func noticeBody(n *message.WorkProgressNotice) string {
	var lines []string
	for _, r := range n.Reports {
		lines = append(lines, fmt.Sprintf("Report %s · work %s · revision %d", r.ReportID, r.WorkID, r.WorkRevision))
	}
	for _, b := range n.Briefs {
		lines = append(lines, "Research delivered · "+string(b.BriefID)+" · work "+string(b.WorkID))
	}
	for _, c := range n.Covered {
		lines = append(lines, fmt.Sprintf("Reporting ended · %s · assignment %d · through revision %d", c.WorkID, c.AssignedAtRevision, c.ThroughRevision))
	}
	return strings.Join(lines, "\n")
}

// The reading view contains the latest narrative. Full identifiers, objective,
// evidence and assignment revisions remain available in the disclosure.
func progressSummary(e work.Event) string {
	if e.Change == nil {
		return e.Work.Task
	}
	var blocks []string
	for _, r := range e.Change.ProgressReports {
		if p := r.Position; p != nil {
			for _, value := range []string{p.Activity, p.Note} {
				if value != "" {
					blocks = append(blocks, value)
				}
			}
			if p.Activity == "" && p.Note == "" && p.Objective != "" {
				blocks = append(blocks, p.Objective)
			}
			for _, field := range [][2]string{{"Next", p.NextStep}, {"Uncertainty", p.Uncertainty}, {"Blocked", p.Blocker}, {"Decision needed", p.DecisionNeed}} {
				if field[1] != "" {
					blocks = append(blocks, progressSummaryField(field[0], field[1]))
				}
			}
		}
		for _, f := range r.Findings {
			blocks = append(blocks, progressField(string(f.Basis), f.Claim))
			if f.Limitation != "" {
				blocks = append(blocks, progressField("Limitation", f.Limitation))
			}
		}
	}
	if len(blocks) == 0 {
		return "Progress updated."
	}
	return strings.Join(blocks, "\n\n")
}

func (m *model) reportExpanded(e *entry) bool {
	if open, ok := m.folds.expanded[foldKey{serial: e.serial}]; ok {
		return open
	}
	return m.folds.allExpanded
}

func (m *model) renderProgress(e *entry, firstRow int) string {
	width := max(1, min(83, m.viewport.Width-1))
	style, label, disclosure := keywordStyle, "↳ Progress", "Report details"
	if e.label == "Work" {
		style, label, disclosure = accentStyle, "◆ Work", "Assignment details"
	}
	heading := style.Bold(true).Render(label)
	if len(e.actors) > 0 {
		column := 2 + ansi.StringWidth(heading) + 1
		heading += " " + agentIcon(e.actors[0])
		m.badges.targets = append(m.badges.targets, agentBadgeTarget{id: e.actors[0], row: firstRow, column: column})
	}
	if e.meta != "" {
		heading += " · " + dimStyle.Render(e.meta)
	}
	inner := max(1, width-2)
	lines := strings.Split(ansi.Wrap(heading, inner, ""), "\n")
	inCode := false
	first := true
	for _, line := range strings.Split(m.renderBodyWidth(e, inner), "\n") {
		plain := strings.TrimSpace(ansi.Strip(line))
		if strings.HasPrefix(plain, "┌─ code") {
			inCode = true
		}
		if plain != "" || inCode {
			if first && !inCode {
				line = lipgloss.NewStyle().Bold(true).Render(line)
			}
			lines = append(lines, line)
			first = false
		}
		if plain == "└─" {
			inCode = false
		}
	}
	if e.reportDetail != nil {
		key := foldKey{serial: e.serial}
		open := m.reportExpanded(e)
		m.folds.targets = append(m.folds.targets, foldTarget{key: key, row: firstRow + len(lines), column: 2})
		lines = append(lines, m.foldMarker(key, open)+dimStyle.Render(" "+disclosure))
		if open {
			lines = append(lines, strings.Split(m.renderBodyWidth(e.reportDetail, inner), "\n")...)
		}
	}
	for i := range lines {
		rail := "│ "
		if i == 0 {
			rail = "┌ "
		} else if i == len(lines)-1 {
			rail = "└ "
		}
		lines[i] = ansi.Truncate(style.Render(rail)+lines[i], max(1, m.viewport.Width-1), "")
	}
	return strings.Join(lines, "\n")
}
