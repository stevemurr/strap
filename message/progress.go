package message

import (
	"encoding/json"
	"errors"
	"github.com/stevemurr/strap/work"
	"slices"
)

const MaxProgressNoticeBytes = 16 * 1024

type ProgressReportRef struct {
	WorkID             work.ID               `json:"work_id"`
	AssignedAtRevision work.Revision         `json:"assigned_at_revision"`
	WorkRevision       work.Revision         `json:"work_revision"`
	ReportID           work.ProgressReportID `json:"report_id"`
}
type ResearchBriefRef struct {
	WorkID             work.ID              `json:"work_id"`
	AssignedAtRevision work.Revision        `json:"assigned_at_revision"`
	WorkRevision       work.Revision        `json:"work_revision"`
	BriefID            work.ResearchBriefID `json:"brief_id"`
}
type ProgressCoverage struct {
	WorkID             work.ID       `json:"work_id"`
	AssignedAtRevision work.Revision `json:"assigned_at_revision"`
	ThroughRevision    work.Revision `json:"through_revision"`
}

// WorkProgressNotice contains locators, never report bodies or model authority.
type WorkProgressNotice struct {
	Reports   []ProgressReportRef `json:"reports,omitempty"`
	Briefs    []ResearchBriefRef  `json:"briefs,omitempty"`
	Covered   []ProgressCoverage  `json:"covered,omitempty"`
	Attention bool                `json:"attention,omitempty"`
}

func (n WorkProgressNotice) Clone() WorkProgressNotice {
	n.Reports = slices.Clone(n.Reports)
	n.Briefs = slices.Clone(n.Briefs)
	n.Covered = slices.Clone(n.Covered)
	return n
}
func (n WorkProgressNotice) Validate() error {
	if len(n.Reports)+len(n.Briefs)+len(n.Covered) == 0 {
		return errors.New("empty progress notice")
	}
	for _, r := range n.Reports {
		if r.WorkID == "" || r.ReportID == "" || r.AssignedAtRevision == 0 || r.WorkRevision < r.AssignedAtRevision {
			return errors.New("invalid report reference")
		}
	}
	for _, r := range n.Briefs {
		if r.WorkID == "" || r.BriefID == "" || r.AssignedAtRevision == 0 || r.WorkRevision < r.AssignedAtRevision {
			return errors.New("invalid brief reference")
		}
	}
	for _, r := range n.Covered {
		if r.WorkID == "" || r.AssignedAtRevision == 0 || r.ThroughRevision < r.AssignedAtRevision {
			return errors.New("invalid coverage reference")
		}
	}
	b, err := json.Marshal(n)
	if err != nil {
		return err
	}
	if len(b) > MaxProgressNoticeBytes {
		return errors.New("progress notice exceeds byte cap")
	}
	return nil
}
