package inspection_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/work"
	"path/filepath"
	"strings"
	"testing"
)

func progressFixture(t *testing.T) (*work.Store, *inspection.ProgressReader, work.Work) {
	t.Helper()
	ctx := context.Background()
	log, err := eventlog.NewJSONL(filepath.Join(t.TempDir(), "trace.jsonl"), "progress")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close(ctx) })
	if _, err = log.Append(ctx, eventlog.Data{Kind: "session_started", Payload: json.RawMessage(`{"id":"progress"}`)}); err != nil {
		t.Fatal(err)
	}
	store := work.New(work.WithReporter(work.ReporterFunc(func(ctx context.Context, e work.Event) error {
		d, err := eventcodec.EncodeEvent(conversation.WorkEvent{Event: e})
		if err != nil {
			return err
		}
		_, err = log.Append(ctx, d)
		return err
	})))
	w, err := store.AssignResearch("root", work.ResearchAssignRequest{Assignee: "worker", Task: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := inspection.New(ctx, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close(ctx) })
	bounded, err := inspection.NewProgressReader(reader)
	if err != nil {
		t.Fatal(err)
	}
	return store, bounded, w
}
func TestProgressFragmentsPreserveEscapedRecordAndBudget(t *testing.T) {
	s, r, w := progressFixture(t)
	ctx := context.Background()
	receipt, err := s.ReportWorkProgress("worker", work.ReportWorkProgressRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Position: &work.WorkPosition{Objective: strings.Repeat("\"\\\n界", 500)}})
	if err != nil {
		t.Fatal(err)
	}
	original, err := s.GetWorkProgressReport("root", receipt.ReportID)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(original)
	q := inspection.ProgressQuery{Mode: "report", ReportID: receipt.ReportID, MaxBytes: 2048}
	var text strings.Builder
	pages := 0
	for {
		p, err := r.Read(ctx, "root", q)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(p)
		if len(b) > 2048 {
			t.Fatal("budget", len(b))
		}
		pages++
		if pages == 1 && p.Oversized == nil {
			t.Fatal("missing oversized descriptor")
		}
		if p.Fragment != nil {
			if p.Fragment.Offset != text.Len() {
				t.Fatal("nonmonotonic fragment")
			}
			text.WriteString(p.Fragment.Text)
		}
		if p.NextCursor == "" {
			break
		}
		if pages > 100 {
			t.Fatal("cursor did not advance")
		}
		q = inspection.ProgressQuery{Mode: "continue", Cursor: p.NextCursor}
	}
	if text.String() != string(want) {
		t.Fatal("record bytes changed")
	}
}
func TestProgressCollectionPinsPrefixAndRejectsCursorMutation(t *testing.T) {
	s, r, w := progressFixture(t)
	ctx := context.Background()
	report := func() {
		result, err := s.ReportWorkProgress("worker", work.ReportWorkProgressRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Findings: []work.ProgressFindingDraft{{Claim: "concern", Basis: work.Inferred, Limitation: "unchecked"}}})
		if err != nil {
			t.Fatal(err)
		}
		w.Revision = result.WorkRevision
	}
	report()
	report()
	first, err := r.Read(ctx, "root", inspection.ProgressQuery{Mode: "reports", WorkID: w.ID, Limit: 1})
	if err != nil || first.NextCursor == "" {
		t.Fatal(first, err)
	}
	report()
	next, err := r.Read(ctx, "root", inspection.ProgressQuery{Mode: "continue", Cursor: first.NextCursor})
	if err != nil || len(next.Items) != 1 || next.NextCursor != "" || next.Through != first.Through {
		t.Fatal(next, err)
	}
	if _, err = r.Read(ctx, "worker", inspection.ProgressQuery{Mode: "continue", Cursor: first.NextCursor}); !errors.Is(err, work.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err = r.Read(ctx, "root", inspection.ProgressQuery{Mode: "continue", Cursor: "x" + first.NextCursor[1:]}); !errors.Is(err, work.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err = r.Read(ctx, "root", inspection.ProgressQuery{Mode: "continue", Cursor: first.NextCursor, MaxBytes: 32768}); !errors.Is(err, work.ErrInvalid) {
		t.Fatal(err)
	}
	reports, err := r.ListWorkProgressReports(ctx, "root", work.ReportQuery{WorkID: w.ID, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.ListWorkProgressFindings(ctx, "root", work.ReportQuery{Cursor: reports.NextCursor}); !errors.Is(err, work.ErrInvalid) {
		t.Fatal("collection cursor changed type", err)
	}
}

func TestLiveProgressRevokesCollectionsAndFragmentsWhileArchiveStaysPassive(t *testing.T) {
	s, r, w := progressFixture(t)
	ctx := context.Background()
	r.Authorize = func(_ context.Context, actor identity.ActorID, id work.ID) error {
		_, err := s.GetWork(actor, id)
		return err
	}
	var id work.ProgressReportID
	for i := 0; i < 2; i++ {
		receipt, err := s.ReportWorkProgress("worker", work.ReportWorkProgressRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Position: &work.WorkPosition{Objective: strings.Repeat("x", 4000)}})
		if err != nil {
			t.Fatal(err)
		}
		w.Revision = receipt.WorkRevision
		id = receipt.ReportID
	}
	collection, err := r.Read(ctx, "worker", inspection.ProgressQuery{Mode: "reports", WorkID: w.ID, Limit: 1})
	if err != nil || collection.NextCursor == "" {
		t.Fatal(collection, err)
	}
	record, err := r.Read(ctx, "worker", inspection.ProgressQuery{Mode: "report", ReportID: id, MaxBytes: 2048})
	if err != nil || record.Oversized == nil {
		t.Fatal(record, err)
	}
	if _, err = s.Reassign("root", work.ReassignRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Assignee: "replacement"}); err != nil {
		t.Fatal(err)
	}
	for _, cursor := range []string{collection.NextCursor, record.NextCursor} {
		if _, err = r.Read(ctx, "worker", inspection.ProgressQuery{Mode: "continue", Cursor: cursor}); !errors.Is(err, work.ErrForbidden) {
			t.Fatal("live cursor retained authority", err)
		}
	}
	archive, err := inspection.NewProgressReader(r.Reader)
	if err != nil {
		t.Fatal(err)
	}
	archive.Through = record.Through
	old, err := archive.Read(ctx, "worker", inspection.ProgressQuery{Mode: "report", ReportID: id})
	if err != nil || len(old.Items) != 1 {
		t.Fatal(old, err)
	}
	if _, err = r.ReadFamily(ctx, "worker", inspection.ProgressQuery{Mode: "continue", Cursor: record.NextCursor}, true); !errors.Is(err, work.ErrInvalid) {
		t.Fatal("brief reader accepted report cursor", err)
	}
}
