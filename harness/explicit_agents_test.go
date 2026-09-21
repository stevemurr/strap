package harness_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/work"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestRegisteredViewsAndWorkDiscoveryReplayAtFixedPrefix(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t, false)
	cfg.Events.JSONLPath = filepath.Join(cfg.Dir, "trace.jsonl")
	s, e := harness.New(ctx, cfg, harness.Dependencies{Provider: textResponse("ready")})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Dispose(ctx)
	impl := createWorker(t, s, roster.Implementor)
	auditor := createWorker(t, s, roster.Auditor)
	in, e := s.InspectAgent(impl, conversation.InspectOptions{})
	if e != nil || !in.Registered || in.Role != roster.Implementor || in.State != agent.Idle || !reflect.DeepEqual(in.EligibleWorkKinds, []work.Kind{work.Implementation, work.Repair}) || len(in.ActiveWorkIDs) != 0 {
		t.Fatal(in, e)
	}
	root, e := s.InspectAgent(s.Root(), conversation.InspectOptions{})
	if e != nil || root.Role != roster.Root || !root.Registered {
		t.Fatal(root, e)
	}
	var works []work.Work
	for i := 0; i < 23; i++ {
		task := strings.Repeat("界", 300)
		if i == 0 {
			task = strings.Repeat("界", 40000)
		}
		w, e := s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.Implementation, Assignee: impl, Task: task})
		if e != nil {
			t.Fatal(e)
		}
		works = append(works, w)
	}
	first, e := s.ListWork(ctx, s.Root(), work.ListQuery{Assignee: impl, State: work.Active, Limit: 2})
	if e != nil || len(first.Items) != 2 || first.NextCursor == "" || first.Items[0].ID != works[22].ID || len([]rune(first.Items[0].TaskPreview)) != 240 {
		t.Fatal(first, e)
	}
	if _, e = s.ListWork(ctx, auditor, work.ListQuery{Cursor: first.NextCursor}); !errors.Is(e, work.ErrForbidden) {
		t.Fatal(e)
	}
	if _, e = s.ListWork(ctx, s.Root(), work.ListQuery{Cursor: first.NextCursor, State: work.Active}); !errors.Is(e, work.ErrInvalid) {
		t.Fatal(e)
	}
	// Change a later-page item and add work after the captured prefix.
	if _, e = s.CancelWork(ctx, s.Root(), work.CancelRequest{WorkTarget: work.WorkTarget{ID: works[0].ID, ExpectedRevision: works[0].Revision}, Reason: "cancel after page one"}); e != nil {
		t.Fatal(e)
	}
	newer, e := s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.Implementation, Assignee: impl, Task: "later"})
	if e != nil {
		t.Fatal(e)
	}
	rest, e := s.ListWork(ctx, s.Root(), work.ListQuery{Cursor: first.NextCursor, Limit: 100})
	if e != nil || len(rest.Items) != 21 {
		t.Fatal(rest, e)
	}
	for _, w := range rest.Items {
		if w.State != work.Active || w.ID == newer.ID {
			t.Fatal("mixed snapshot", w)
		}
	}
	if rest.Items[len(rest.Items)-1].ID != works[0].ID {
		t.Fatal("missing original item")
	}
	cancelled, e := s.ListWork(ctx, s.Root(), work.ListQuery{State: work.Cancelled})
	if e != nil || len(cancelled.Items) != 1 {
		t.Fatal(cancelled, e)
	}
	// The complete default page is bounded and includes non-active states.
	defaultPage, e := s.ListWork(ctx, s.Root(), work.ListQuery{})
	if e != nil || len(defaultPage.Items) != 20 || defaultPage.NextCursor == "" {
		t.Fatal(defaultPage, e)
	}
	submitted, e := s.SubmitWork(ctx, impl, work.SubmitRequest{WorkTarget: work.WorkTarget{ID: newer.ID, ExpectedRevision: newer.Revision}, Summary: "ready"})
	if e != nil {
		t.Fatal(e)
	}
	in, e = s.InspectAgent(impl, conversation.InspectOptions{})
	if e != nil || slices.Contains(in.ActiveWorkIDs, newer.ID) || slices.Contains(in.ActiveWorkIDs, works[0].ID) {
		t.Fatal(in, e)
	}
	needs, e := s.ListWork(ctx, s.Root(), work.ListQuery{State: work.NeedsCheck})
	if e != nil || len(needs.Items) != 1 || needs.Items[0].LatestSubmissionID != submitted.ID {
		t.Fatal(needs, e)
	}
	pending, _ := s.GetWork(ctx, s.Root(), newer.ID)
	review, e := s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.AuditWork, Assignee: auditor, WorkID: newer.ID, ExpectedRevision: pending.Revision, SubmissionID: submitted.ID})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.SubmitAudit(ctx, auditor, work.AuditRequest{WorkTarget: work.WorkTarget{ID: review.ID, ExpectedRevision: review.Revision}, SubmissionID: submitted.ID, Verdict: work.Pass, Summary: "verified"}); e != nil {
		t.Fatal(e)
	}
	accepted, e := s.ListWork(ctx, s.Root(), work.ListQuery{State: work.Accepted})
	if e != nil || len(accepted.Items) != 1 || accepted.Items[0].ID != newer.ID {
		t.Fatal("accepted work was not discoverable", accepted, e)
	}
	// Opaque cursors cannot select other sessions, future prefixes, or invented keys.
	raw, _ := base64.RawURLEncoding.DecodeString(first.NextCursor)
	var cursor map[string]any
	json.Unmarshal(raw, &cursor)
	for key, value := range map[string]any{"session": "elsewhere", "prefix": float64(1 << 50), "id": "missing", "sequence": float64(1)} {
		changed := map[string]any{}
		for k, v := range cursor {
			changed[k] = v
		}
		changed[key] = value
		b, _ := json.Marshal(changed)
		if _, e = s.ListWork(ctx, s.Root(), work.ListQuery{Cursor: base64.RawURLEncoding.EncodeToString(b)}); e == nil {
			t.Fatal("accepted forged cursor", key)
		}
	}
	if e = s.Close(ctx); e != nil {
		t.Fatal(e)
	}
	reader, e := inspection.OpenJSONL(ctx, cfg.Events.JSONLPath)
	if e != nil {
		t.Fatal(e)
	}
	defer reader.Close(ctx)
	archived, e := reader.ListWork(ctx, s.Root(), work.ListQuery{Cursor: first.NextCursor, Limit: 100})
	if e != nil || !reflect.DeepEqual(archived, rest) {
		t.Fatal("archive snapshot differs", e)
	}
	view, e := reader.At(ctx, eventlog.Cursor{})
	if e != nil {
		t.Fatal(e)
	}
	archiveAgent, e := view.InspectAgent(ctx, impl)
	if e != nil || archiveAgent.Role != roster.Implementor || !archiveAgent.Registered {
		t.Fatal(archiveAgent, e)
	}
	live, e := s.InspectAgent(impl, conversation.InspectOptions{})
	if e != nil || !reflect.DeepEqual(live, archiveAgent) {
		t.Fatal("live/archive role views differ", e)
	}
	empty, e := reader.ListWork(ctx, s.Root(), work.ListQuery{Assignee: auditor, State: work.Active})
	if e != nil || empty.Items == nil || len(empty.Items) != 0 || empty.NextCursor != "" {
		t.Fatal(empty, e)
	}
}
