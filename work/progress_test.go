package work

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stevemurr/strap/identity"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func reportRequest(w Work) ReportWorkProgressRequest {
	return ReportWorkProgressRequest{WorkID: w.ID}
}
func observed(claim string) ProgressFindingDraft {
	return ProgressFindingDraft{Claim: claim, Basis: Observed, Evidence: []EvidenceRef{{URI: "file:board.go", Detail: "parser input"}}}
}
func mustReport(t *testing.T, s *Store, w Work, r ReportWorkProgressRequest) ReportWorkProgressResult {
	t.Helper()
	v, err := s.ReportWorkProgress(w.Assignee, r)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func current(t *testing.T, s *Store, id ID) Work {
	t.Helper()
	w, err := s.GetWork("root", id)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestProgressAtomicStepsAndImmutableHistory(t *testing.T) {
	s, p, w := fixture(t)
	r := reportRequest(w)
	r.Position = &WorkPosition{Objective: "establish build state", Note: "source inspection", Dependencies: []ProgressDependency{{Need: "a test result"}}}
	r.Findings = []ProgressFindingDraft{observed("syntax error in board.go")}
	r.Steps = []StepProgress{{ID: p.Steps[0].ID, Status: ptr(InProgress)}}
	receipt := mustReport(t, s, w, r)
	r.Position.Dependencies[0].Need = "mutated input"
	r.Findings[0].Evidence[0].URI = "mutated input"
	*r.Steps[0].Status = Completed
	got, err := s.GetWorkProgress("impl", w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Current.WorkRevision != w.Revision+1 || got.Steps[0].Status != InProgress || got.Current.CurrentNote != "source inspection" {
		t.Fatalf("snapshot: %+v", got)
	}
	if got.LastReportedPosition.Value.Dependencies[0].Need != "a test result" || got.Findings[0].Evidence[0].URI != "file:board.go" {
		t.Fatal("input aliases stored report")
	}
	plan, _ := s.GetPlan("root", p.ID)
	if plan.Revision != p.Revision || plan.Steps[0].Status != InProgress || plan.Steps[2].Status != Pending {
		t.Fatal("progress changed structure or unrelated step")
	}
	got.Findings[0].Evidence[0].URI = "mutated output"
	stored, _ := s.GetWorkProgressReport("root", receipt.ReportID)
	if stored.Findings[0].Evidence[0].URI != "file:board.go" {
		t.Fatal("output aliases stored report")
	}
	// The same report sent twice no longer collides on a revision; its step
	// transitions are what reject it.
	if _, err := s.ReportWorkProgress("impl", r); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate: %v", err)
	}

	w = current(t, s, w.ID)
	r = reportRequest(w)
	r.Steps = []StepProgress{{ID: p.Steps[1].ID, Status: ptr(InProgress)}}
	second := mustReport(t, s, w, r)
	got, _ = s.GetWorkProgress("root", w.ID)
	if got.LatestReportID != second.ReportID || got.LastReportedPosition.ReportID != receipt.ReportID || !got.LastReportedPosition.RecordedAt.Equal(receipt.RecordedAt) {
		t.Fatal("step update refreshed position")
	}

	w = current(t, s, w.ID)
	r = reportRequest(w)
	r.Position = &WorkPosition{Objective: "new phase"}
	mustReport(t, s, w, r)
	got, _ = s.GetWorkProgress("root", w.ID)
	if got.Current.CurrentNote != "" || len(got.Findings) != 1 {
		t.Fatal("replacement failed to clear note or preserve findings")
	}
}

func TestProgressCorrectionAndAtomicRejection(t *testing.T) {
	s, p, w := fixture(t)
	r := reportRequest(w)
	r.Findings = []ProgressFindingDraft{observed("first claim")}
	first := mustReport(t, s, w, r)
	w = current(t, s, w.ID)
	r = reportRequest(w)
	f := observed("corrected claim")
	f.Supersedes = first.FindingIDs[0]
	r.Findings = []ProgressFindingDraft{f, f}
	r.Steps = []StepProgress{{ID: p.Steps[0].ID, Status: ptr(InProgress)}}
	before := len(s.PendingEvents(0))
	if _, err := s.ReportWorkProgress("impl", r); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate correction: %v", err)
	}
	got, _ := s.GetWorkProgress("root", w.ID)
	if got.Current.WorkRevision != w.Revision || got.Steps[0].Status != Pending || len(s.PendingEvents(0)) != before {
		t.Fatal("rejected report partially committed")
	}
	r.Findings = r.Findings[:1]
	second := mustReport(t, s, w, r)
	got, _ = s.GetWorkProgress("root", w.ID)
	if len(got.Findings) != 1 || got.Findings[0].ID != second.FindingIDs[0] {
		t.Fatal("current findings failed to supersede")
	}
	old, _ := s.GetProgressFinding("root", first.FindingIDs[0])
	if old.Claim != "first claim" {
		t.Fatal("old finding rewritten")
	}
	w = current(t, s, w.ID)
	r = reportRequest(w)
	r.Findings = []ProgressFindingDraft{{Claim: "check was invalid", Basis: Retracted, Supersedes: second.FindingIDs[0]}}
	mustReport(t, s, w, r)
	got, _ = s.GetWorkProgress("root", w.ID)
	if len(got.Findings) != 1 || got.Findings[0].Basis != Retracted {
		t.Fatal("missing retraction")
	}
}

func TestProgressReassignmentAndTerminalScopedRead(t *testing.T) {
	s, _, w := fixture(t)
	r := reportRequest(w)
	r.Position = &WorkPosition{Objective: "investigate", Blocker: "need help", Note: "old worker"}
	r.Findings = []ProgressFindingDraft{observed("inherited finding")}
	first := mustReport(t, s, w, r)
	w = current(t, s, w.ID)
	w, err := s.Reassign("root", ReassignRequest{WorkTarget: target(w), Assignee: "replacement"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetWorkProgress("replacement", w.ID)
	if got.LastReportedPosition != nil || got.LatestReportedAt != nil || got.Current.ActiveBlocker != "" || got.Current.CurrentNote != "" || len(got.Findings) != 1 {
		t.Fatalf("reassignment snapshot: %+v", got)
	}
	if _, err = s.GetWorkProgressReport("impl", first.ReportID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("former assignee read: %v", err)
	}
	w, err = s.Reassign("root", ReassignRequest{WorkTarget: target(w), Assignee: "impl"})
	if err != nil {
		t.Fatal(err)
	}
	r = reportRequest(w)
	r.Position = &WorkPosition{Objective: "new investigation", Blocker: "still blocked"}
	mustReport(t, s, w, r)
	w = current(t, s, w.ID)
	w, err = s.Cancel("root", CancelRequest{WorkTarget: target(w), Reason: "stop"})
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.GetWorkProgress("impl", w.ID)
	if err != nil || len(got.Steps) != 2 || got.Current.ActiveBlocker != "" || got.LastReportedPosition.Value.Blocker != "still blocked" {
		t.Fatalf("terminal snapshot: %+v %v", got, err)
	}
	r = reportRequest(w)
	r.Position = &WorkPosition{Objective: "late"}
	if _, err = s.ReportWorkProgress("impl", r); !errors.Is(err, ErrState) {
		t.Fatalf("terminal report: %v", err)
	}
}

func TestProgressPublicationAndPassiveReplay(t *testing.T) {
	var events []Event
	fail := false
	s := New(WithReporter(ReporterFunc(func(_ context.Context, e Event) error {
		if fail {
			return errors.New("recording unavailable")
		}
		events = append(events, e.Clone())
		return nil
	})))
	w, err := s.AssignWork("root", AssignRequest{Assignee: "impl", Task: "investigate"})
	if err != nil {
		t.Fatal(err)
	}
	r := reportRequest(w)
	r.Position = &WorkPosition{Objective: "read code"}
	r.Findings = []ProgressFindingDraft{observed("finding")}
	first := mustReport(t, s, w, r)
	view := NewReadModel()
	for _, e := range events {
		b, err := json.Marshal(e.Change)
		if err != nil {
			t.Fatal(err)
		}
		var c Change
		if err = json.Unmarshal(b, &c); err != nil {
			t.Fatal(err)
		}
		view.Apply(c)
	}
	live, _ := s.GetWorkProgress("root", w.ID)
	replayed, _ := view.GetWorkProgress("root", w.ID)
	if !reflect.DeepEqual(live, replayed) {
		t.Fatalf("replay mismatch: %+v / %+v", live, replayed)
	}
	stored, _ := view.GetWorkProgressReport("root", first.ReportID)
	if stored.Findings[0].Claim != "finding" {
		t.Fatal("report omitted in replay")
	}
	w = current(t, s, w.ID)
	r = reportRequest(w)
	r.Position = &WorkPosition{Objective: "second"}
	before := len(s.PendingEvents(0))
	fail = true
	if _, err = s.ReportWorkProgress("impl", r); err == nil {
		t.Fatal("publication failure returned success")
	}
	if len(s.PendingEvents(0)) != before {
		t.Fatal("unpublished event became dispatchable")
	}
	if _, err = s.ReportWorkProgress("impl", r); err == nil {
		t.Fatal("failed store allowed another mutation")
	}
}

func TestProgressRejectsInvalidReports(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ReportWorkProgressRequest, Work)
		want   error
	}{
		{"empty", func(r *ReportWorkProgressRequest, _ Work) {}, ErrInvalid},
		{"missing objective", func(r *ReportWorkProgressRequest, _ Work) { r.Position = &WorkPosition{} }, ErrInvalid},
		{"missing evidence", func(r *ReportWorkProgressRequest, _ Work) {
			r.Findings = []ProgressFindingDraft{{Claim: "observed", Basis: Observed}}
		}, ErrInvalid},
		{"unqualified inference", func(r *ReportWorkProgressRequest, _ Work) {
			r.Findings = []ProgressFindingDraft{{Claim: "inferred", Basis: Inferred}}
		}, ErrInvalid},
		{"long prose", func(r *ReportWorkProgressRequest, _ Work) {
			r.Position = &WorkPosition{Objective: strings.Repeat("x", 4097)}
		}, ErrInvalid},
		{"accept step", func(r *ReportWorkProgressRequest, w Work) {
			r.Steps = []StepProgress{{ID: w.Scope.StepIDs[0], Status: ptr(Completed)}}
		}, ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, w := fixture(t)
			r := reportRequest(w)
			tc.mutate(&r, w)
			before := len(s.PendingEvents(0))
			if _, err := s.ReportWorkProgress("impl", r); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if current(t, s, w.ID).Revision != w.Revision || len(s.PendingEvents(0)) != before {
				t.Fatal("invalid mutation changed ledger")
			}
		})
	}
}

// Reports no longer contend for a revision, so concurrent ones all record. The
// property that matters is that none of them is lost or corrupted: the store
// serializes them and every finding survives. A model that repeats an identical
// report now records it twice, which its own repeated-call detection notices.
func TestProgressConcurrentReportsAllRecord(t *testing.T) {
	s, _, w := fixture(t)
	r := reportRequest(w)
	r.Findings = []ProgressFindingDraft{observed("one observation")}
	var group sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := s.ReportWorkProgress("impl", r)
			results <- err
		}()
	}
	group.Wait()
	close(results)
	recorded := 0
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
		recorded++
	}
	got, _ := s.GetWorkProgress("root", w.ID)
	if recorded != 8 || len(got.Findings) != 8 || got.Current.WorkRevision != w.Revision+8 {
		t.Fatalf("recorded=%d snapshot=%+v", recorded, got)
	}
}

func TestProgressAuditAuthorityAndEncodedLimit(t *testing.T) {
	s, p, w := fixture(t)
	w = ready(t, s, w)
	sub := submit(t, s, w)
	a := review(t, s, w.ID, sub.ID)
	r := reportRequest(a)
	r.Position = &WorkPosition{Objective: "verify submission", Blocker: "missing environment"}
	r.Steps = []StepProgress{{ID: p.Steps[0].ID, Status: ptr(InProgress)}}
	if _, err := s.ReportWorkProgress(a.Assignee, r); !errors.Is(err, ErrForbidden) {
		t.Fatalf("auditor steps: %v", err)
	}
	r.Steps = nil
	mustReport(t, s, a, r)
	if current(t, s, w.ID).State != Checking {
		t.Fatal("auditor progress became a verdict")
	}
	a = current(t, s, a.ID)
	r = reportRequest(a)
	for range 16 {
		r.Findings = append(r.Findings, observed(strings.Repeat("x", 4096)))
	}
	before := len(s.PendingEvents(0))
	if _, err := s.ReportWorkProgress(a.Assignee, r); !errors.Is(err, ErrInvalid) {
		t.Fatalf("encoded report cap: %v", err)
	}
	if len(s.PendingEvents(0)) != before || current(t, s, a.ID).Revision != a.Revision {
		t.Fatal("oversized report committed")
	}
}

// Lifecycle fixtures explicitly supply the new report contract, then inspect
// the changed work when subsequent assertions need more than its receipt.
func (s *Store) reportSnapshot(actor identity.ActorID, r ReportWorkProgressRequest) (Work, error) {
	_, err := s.ReportWorkProgress(actor, r)
	if err != nil {
		return Work{}, err
	}
	return s.GetWork(actor, r.WorkID)
}
