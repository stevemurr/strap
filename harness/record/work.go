package record

import "github.com/stevemurr/strap/work"

// WorkControl keeps changed entity identities and statuses inline even when the
// complete ledger values and evidence are framed into content chunks.
type WorkHeader struct {
	ID       work.ID       `json:"id"`
	Kind     work.Kind     `json:"kind"`
	State    work.State    `json:"state"`
	Revision work.Revision `json:"revision"`
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
type WorkControl struct {
	ID          work.EventID       `json:"event_id"`
	Kind        work.EventKind     `json:"kind"`
	Works       []WorkHeader       `json:"works,omitempty"`
	Plans       []PlanHeader       `json:"plans,omitempty"`
	Submissions []SubmissionHeader `json:"submissions,omitempty"`
	Audits      []AuditHeader      `json:"audits,omitempty"`
}

func DescribeWork(e work.Event) WorkControl {
	c := WorkControl{ID: e.ID, Kind: e.Kind}
	change := work.Change{}
	if e.Change != nil {
		change = *e.Change
	} else {
		if e.Work.ID != "" {
			change.Works = append(change.Works, e.Work)
		}
		if e.Plan != nil {
			change.Plans = append(change.Plans, *e.Plan)
		}
	}
	for _, w := range change.Works {
		c.Works = append(c.Works, WorkHeader{ID: w.ID, Kind: w.Kind, State: w.State, Revision: w.Revision})
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
	return c
}
