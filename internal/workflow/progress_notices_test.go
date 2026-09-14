package workflow

import (
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
	"strings"
	"testing"
	"time"
)

func reportEvent(id string) work.Event {
	return work.Event{ID: work.EventID(id), Kind: work.WorkProgressReported, Work: work.Work{ID: work.ID(id), Owner: "root", Assignee: "worker", Revision: 2, AssignedAtRevision: 1, LatestProgressReportID: work.ProgressReportID(id)}}
}
func TestNoticeQueueTimingAndImmutableBatches(t *testing.T) {
	now := time.Unix(100, 0)
	q := newNoticeQueue(WorkProgressReportingConfig{2 * time.Second, 15 * time.Second, 2})
	q.add(reportEvent("a"), now)
	q.add(reportEvent("b"), now)
	var got []message.WorkProgressNotice
	send := func(_ identity.ActorID, n message.WorkProgressNotice, ids []work.EventID) bool {
		if len(ids) != len(n.Reports) {
			t.Fatal(ids, n)
		}
		got = append(got, n)
		return true
	}
	q.flush(now.Add(time.Second), send)
	if len(got) != 0 {
		t.Fatal("early notice")
	}
	q.flush(now.Add(2*time.Second), send)
	q.add(reportEvent("c"), now.Add(3*time.Second))
	q.add(reportEvent("d"), now.Add(3*time.Second))
	q.add(reportEvent("e"), now.Add(3*time.Second))
	q.flush(now.Add(16*time.Second), send)
	if len(got) != 1 || len(got[0].Reports) != 2 {
		t.Fatal(got)
	}
	q.flush(now.Add(17*time.Second), send)
	q.flush(now.Add(32*time.Second), send)
	if len(got) != 3 || got[2].Reports[0].ReportID != "e" || !q.next().IsZero() {
		t.Fatal(got, q.next())
	}
}
func TestNoticeQueueByteBoundAndRetry(t *testing.T) {
	q := newNoticeQueue(WorkProgressReportingConfig{0, time.Second, 16})
	now := time.Unix(0, 0)
	a := reportEvent("a")
	a.Work.LatestProgressReportID = work.ProgressReportID(strings.Repeat("x", 9000))
	q.add(a, now)
	q.add(a, now)
	count := 0
	send := func(_ identity.ActorID, n message.WorkProgressNotice, _ []work.EventID) bool {
		count++
		if n.Validate() != nil || len(n.Reports) != 1 {
			t.Fatal(n)
		}
		return count > 1
	}
	q.flush(now, send)
	q.flush(now.Add(time.Second), send)
	q.flush(now.Add(2*time.Second), send)
	if count != 3 || !q.next().IsZero() {
		t.Fatal(count)
	}
	for _, c := range []WorkProgressReportingConfig{{-1, 0, 1}, {0, -1, 1}, {0, 0, 0}} {
		if c.Validate() == nil {
			t.Fatal(c)
		}
	}
}
