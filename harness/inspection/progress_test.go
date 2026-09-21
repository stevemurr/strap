package inspection_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/work"
)

func TestProgressRecordedPrefixAndArchive(t *testing.T) {
	ctx := context.Background()
	log, path := sessionLog(t, "progress-session")
	var prefix eventlog.Cursor
	s := reportingStore(log, &prefix)
	w, err := s.AssignWork("root", work.AssignRequest{Assignee: "worker", Task: "inspect source"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.ReportWorkProgress("worker", work.ReportWorkProgressRequest{WorkID: w.ID, Position: &work.WorkPosition{Objective: "establish state"}, Findings: []work.ProgressFindingDraft{{Claim: "a limitation", Basis: work.Inferred, Limitation: "not executed"}}})
	if err != nil {
		t.Fatal(err)
	}
	firstPrefix := prefix
	reader, err := inspection.New(ctx, log)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(ctx)
	pinned, err := reader.At(ctx, firstPrefix)
	if err != nil {
		t.Fatal(err)
	}
	before, err := pinned.GetWorkProgress(ctx, "worker", w.ID)
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Reassign("root", work.ReassignRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: first.WorkRevision}, Assignee: "replacement"})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := reader.At(ctx, eventlog.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := latest.GetWorkProgressReport(ctx, "worker", first.ReportID); !errors.Is(err, work.ErrForbidden) {
		t.Fatalf("current access after reassignment: %v", err)
	}
	current, err := latest.GetWorkProgress(ctx, "replacement", w.ID)
	if err != nil || current.LastReportedPosition != nil || len(current.Findings) != 1 {
		t.Fatalf("current snapshot: %+v %v", current, err)
	}
	if err := log.Seal(ctx, eventlog.Outcome{Reason: "requested"}); err != nil {
		t.Fatal(err)
	}
	archive, err := inspection.OpenJSONL(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close(ctx)
	old, err := archive.At(ctx, firstPrefix)
	if err != nil {
		t.Fatal(err)
	}
	after, err := old.GetWorkProgress(ctx, "worker", w.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("archive mismatch: %+v / %+v (%v)", before, after, err)
	}
	finding, err := old.GetProgressFinding(ctx, "root", first.FindingIDs[0])
	if err != nil || finding.Claim != "a limitation" {
		t.Fatalf("exact finding: %+v %v", finding, err)
	}
}
