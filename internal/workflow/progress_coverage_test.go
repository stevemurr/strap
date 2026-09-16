package workflow

import (
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
	"testing"
	"time"
)

func TestCoverageIncludesSecondaryWorkAndOldBinding(t *testing.T) {
	prior := map[work.ID]work.Work{}
	var before, after []work.Work
	for i, kind := range []work.Kind{work.Implementation, work.Repair, work.AuditWork, work.AuditWork, work.Research, work.Research, work.Research} {
		w := work.Work{ID: work.ID(string(rune('a' + i))), Kind: kind, State: work.Active, Revision: 2, AssignedAtRevision: 1}
		before = append(before, w)
		w.Revision = 3
		switch i {
		case 0:
			w.State = work.NeedsCheck
		case 1, 2, 3:
			w.State = work.Closed
		case 4:
			w.State = work.Delivered
		case 5:
			w.State = work.Cancelled
		case 6:
			w.AssignedAtRevision = 3
		}
		after = append(after, w)
	}
	coverageChange(prior, &work.Change{Works: before})
	covered := coverageChange(prior, &work.Change{Works: after})
	if len(covered) != 7 {
		t.Fatal(covered)
	}
	for _, c := range covered {
		if c.AssignedAtRevision != 1 || c.ThroughRevision != 3 {
			t.Fatal(c)
		}
	}
	if len(coverageChange(prior, &work.Change{Works: after})) != 0 {
		t.Fatal("duplicate coverage")
	}
}
func TestRetireBeforeTimerAndClassifyQueuedNotice(t *testing.T) {
	q := newNoticeQueue(DefaultWorkProgressReporting())
	now := time.Unix(0, 0)
	q.add(reportEvent("a"), now)
	q.add(reportEvent("b"), now)
	get := func(_ identity.ActorID, id work.ID) (work.Work, error) {
		w := reportEvent(string(id)).Work
		w.State = work.Active
		if id == "a" {
			w.State = work.Delivered
		}
		return w, nil
	}
	var ack []work.EventID
	if err := q.retire(get, func(id work.EventID) { ack = append(ack, id) }); err != nil {
		t.Fatal(err)
	}
	q.flush(now.Add(2*time.Second), func(_ identity.ActorID, n message.WorkProgressNotice, _ []work.EventID) bool {
		if len(n.Reports) != 1 || n.Reports[0].WorkID != "b" {
			t.Fatal(n)
		}
		return true
	})
	if len(ack) != 1 || ack[0] != "a" {
		t.Fatal(ack)
	}
	// A report-only notice never wakes its owner, whatever the work state.
	for _, id := range []work.ID{"a", "b"} {
		m := message.Message{To: "root", Kind: message.Notification, Progress: &message.WorkProgressNotice{Reports: []message.ProgressReportRef{{WorkID: id, AssignedAtRevision: 1}}}}
		if ProgressNoticeWakes(m) {
			t.Fatal("report-only notice woke the owner", id)
		}
	}
	if !ProgressNoticeWakes(message.Message{Kind: message.Notification, Content: "authored"}) {
		t.Fatal("arbitrary notice suppressed")
	}
}

// Routine findings cost the owner nothing; only a notice that needs a decision
// starts an exchange. Before this rule, half of every root model call in a
// ladder run was spent waking on progress the owner could not act on.
func TestOnlyAttentionBearingNoticesWakeTheOwner(t *testing.T) {
	refs := []message.ProgressReportRef{{WorkID: "w", AssignedAtRevision: 1}}
	notice := func(n message.WorkProgressNotice) message.Message {
		return message.Message{To: "root", Kind: message.Notification, Progress: &n}
	}
	for _, c := range []struct {
		name string
		m    message.Message
		wake bool
	}{
		{"routine findings", notice(message.WorkProgressNotice{Reports: refs}), false},
		{"blocker or decision need", notice(message.WorkProgressNotice{Reports: refs, Attention: true}), true},
		{"research delivered", notice(message.WorkProgressNotice{Briefs: []message.ResearchBriefRef{{WorkID: "w"}}}), true},
		{"assignment ended", notice(message.WorkProgressNotice{Covered: []message.ProgressCoverage{{WorkID: "w"}}}), true},
		{"work event attached", message.Message{To: "root", Kind: message.Notification, Progress: &message.WorkProgressNotice{Reports: refs}, Event: &work.Event{Kind: work.ReviewRequested}}, true},
		{"observation", message.Message{To: "root", Kind: message.Observation, Progress: &message.WorkProgressNotice{Reports: refs, Attention: true}}, false},
		{"worker message", message.Message{To: "root", Kind: message.Instruction, Content: "question"}, true},
	} {
		if got := ProgressNoticeWakes(c.m); got != c.wake {
			t.Errorf("%s: wake=%v, want %v", c.name, got, c.wake)
		}
	}
}
