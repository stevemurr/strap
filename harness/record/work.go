package record

import (
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/work"
)

// WorkControl keeps changed entity identities and statuses inline even when the
// complete ledger values and evidence are framed into content chunks.
type WorkHeader struct {
	Assignee           identity.ActorID `json:"assignee,omitempty"`
	AssignedAtRevision work.Revision    `json:"assigned_at_revision,omitempty"`
	ID                 work.ID          `json:"id"`
	Kind               work.Kind        `json:"kind"`
	State              work.State       `json:"state"`
	Revision           work.Revision    `json:"revision"`
}
type PlanHeader struct {
	ID       work.PlanID   `json:"id"`
	Revision work.Revision `json:"revision"`
}
type SubmissionHeader struct {
	ID   work.SubmissionID `json:"id"`
	Work work.ID           `json:"work"`
}
type AuditHeader struct {
	ID         work.AuditID      `json:"id"`
	Work       work.ID           `json:"work"`
	Submission work.SubmissionID `json:"submission"`
	Verdict    work.Verdict      `json:"verdict"`
}
type ProgressReportHeader struct {
	ID                 work.ProgressReportID    `json:"id"`
	Work               work.ID                  `json:"work"`
	Revision           work.Revision            `json:"revision"`
	AssignedAtRevision work.Revision            `json:"assigned_at_revision"`
	Findings           []work.ProgressFindingID `json:"findings,omitempty"`
}
type ResearchBriefHeader struct {
	ID                 work.ResearchBriefID `json:"id"`
	Work               work.ID              `json:"work"`
	Revision           work.Revision        `json:"revision"`
	AssignedAtRevision work.Revision        `json:"assigned_at_revision"`
}
type WorkControl struct {
	ResearchBriefs  []ResearchBriefHeader  `json:"research_briefs,omitempty"`
	ID              work.EventID           `json:"event_id"`
	Kind            work.EventKind         `json:"kind"`
	Works           []WorkHeader           `json:"works,omitempty"`
	Plans           []PlanHeader           `json:"plans,omitempty"`
	Submissions     []SubmissionHeader     `json:"submissions,omitempty"`
	Audits          []AuditHeader          `json:"audits,omitempty"`
	ProgressReports []ProgressReportHeader `json:"progress_reports,omitempty"`
}

func DescribeWork(e work.Event) WorkControl {
	c := WorkControl{ID: e.ID, Kind: e.Kind}
	change := e.Changes()
	for _, w := range change.Works {
		c.Works = append(c.Works, WorkHeader{Assignee: w.Assignee, AssignedAtRevision: w.AssignedAtRevision, ID: w.ID, Kind: w.Kind, State: w.State, Revision: w.Revision})
	}
	for _, p := range change.Plans {
		c.Plans = append(c.Plans, PlanHeader{ID: p.ID, Revision: p.Revision})
	}
	for _, s := range change.Submissions {
		c.Submissions = append(c.Submissions, SubmissionHeader{ID: s.ID, Work: s.WorkID})
	}
	for _, a := range change.Audits {
		c.Audits = append(c.Audits, AuditHeader{ID: a.ID, Work: a.WorkID, Submission: a.SubmissionID, Verdict: a.Verdict})
	}
	for _, r := range change.ProgressReports {
		h := ProgressReportHeader{ID: r.ID, Work: r.WorkID, Revision: r.WorkRevision, AssignedAtRevision: r.AssignedAtRevision}
		for _, f := range r.Findings {
			h.Findings = append(h.Findings, f.ID)
		}
		c.ProgressReports = append(c.ProgressReports, h)
	}
	for _, b := range change.ResearchBriefs {
		c.ResearchBriefs = append(c.ResearchBriefs, ResearchBriefHeader{b.ID, b.WorkID, b.WorkRevision, b.AssignedAtRevision})
	}
	return c
}
