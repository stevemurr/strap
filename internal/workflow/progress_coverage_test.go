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
	for _, id := range []work.ID{"a", "b"} {
		m := message.Message{To: "root", Kind: message.Notification, Progress: &message.WorkProgressNotice{Reports: []message.ProgressReportRef{{WorkID: id, AssignedAtRevision: 1}}}}
		wake, err := ProgressNoticeWakes(m, get)
		if err != nil || wake != (id == "b") {
			t.Fatal(id, wake, err)
		}
	}
	wake, _ := ProgressNoticeWakes(message.Message{Kind: message.Notification, Content: "authored"}, get)
	if !wake {
		t.Fatal("arbitrary notice suppressed")
	}
}
