package message

import (
	"github.com/stevemurr/strap/work"
	"testing"
)

func TestProgressEnvelopeCloneAndExclusivity(t *testing.T) {
	n := &WorkProgressNotice{Reports: []ProgressReportRef{{WorkID: "w", AssignedAtRevision: 1, WorkRevision: 2, ReportID: "r"}}}
	d := Draft{Kind: Notification, Progress: n}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	m := Message{Progress: n}
	clone := m.Clone()
	clone.Progress.Reports[0].ReportID = work.ProgressReportID("changed")
	if n.Reports[0].ReportID != "r" {
		t.Fatal("shared refs")
	}
	d.Content = "forged"
	if d.Validate() == nil {
		t.Fatal("mixed content accepted")
	}
	d.Content = ""
	d.Kind = Reply
	if d.Validate() == nil {
		t.Fatal("reply accepted")
	}
}
