package tui

import (
	"fmt"
	"strings"

	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
)

// Keep report fields as distinct Markdown paragraphs. Multiline content starts
// its own block so an opening list, heading, or code fence keeps its meaning.
func progressField(label, value string) string {
	separator := " "
	if strings.Contains(value, "\n") {
		separator = "\n\n"
	}
	return "**" + label + ":**" + separator + value
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
