package workflow

import (
	"errors"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
	"time"
)

type WorkProgressReportingConfig struct {
	BatchWindow         time.Duration `json:"batch_window"`
	MinInterval         time.Duration `json:"min_interval"`
	MaxReportsPerNotice int           `json:"max_reports_per_notice"`
}

func DefaultWorkProgressReporting() WorkProgressReportingConfig {
	return WorkProgressReportingConfig{2 * time.Second, 15 * time.Second, 16}
}
func (c WorkProgressReportingConfig) Validate() error {
	if c.BatchWindow < 0 || c.MinInterval < 0 || c.MaxReportsPerNotice <= 0 {
		return errors.New("invalid work progress reporting configuration")
	}
	return nil
}
func WithProgressReporting(c WorkProgressReportingConfig) Option {
	return func(s *Session) { s.progressConfig = c }
}

type pendingProgress struct {
	event work.EventID
	ref   message.ProgressReportRef
}
type ownerProgress struct {
	pending   []pendingProgress
	due, last time.Time
}

// noticeQueue is confined to the workflow dispatcher. Time is supplied by its
// caller so batching and interval boundaries have deterministic tests.
type noticeQueue struct {
	config WorkProgressReportingConfig
	owners map[identity.ActorID]*ownerProgress
}

func newNoticeQueue(c WorkProgressReportingConfig) *noticeQueue {
	return &noticeQueue{c, map[identity.ActorID]*ownerProgress{}}
}
func (q *noticeQueue) add(e work.Event, now time.Time) {
	o := q.owners[e.Work.Owner]
	if o == nil {
		o = &ownerProgress{}
		q.owners[e.Work.Owner] = o
	}
	o.pending = append(o.pending, pendingProgress{e.ID, message.ProgressReportRef{WorkID: e.Work.ID, AssignedAtRevision: e.Work.AssignedAtRevision, WorkRevision: e.Work.Revision, ReportID: e.Work.LatestProgressReportID}})
	if o.due.IsZero() {
		o.due = now.Add(q.config.BatchWindow)
		if next := o.last.Add(q.config.MinInterval); next.After(o.due) {
			o.due = next
		}
	}
}
func (q *noticeQueue) next() time.Time {
	var t time.Time
	for _, o := range q.owners {
		if !o.due.IsZero() && (t.IsZero() || o.due.Before(t)) {
			t = o.due
		}
	}
	return t
}

// flush removes only references actually accepted by the inbox. New reports can
// never mutate a previously queued message. A failed route remains pending.
func (q *noticeQueue) flush(now time.Time, send func(identity.ActorID, message.WorkProgressNotice, []work.EventID) bool) {
	for owner, o := range q.owners {
		if o.due.IsZero() || now.Before(o.due) {
			continue
		}
		n := message.WorkProgressNotice{}
		var ids []work.EventID
		for _, p := range o.pending {
			if len(ids) >= q.config.MaxReportsPerNotice {
				break
			}
			n.Reports = append(n.Reports, p.ref)
			if n.Validate() != nil {
				n.Reports = n.Reports[:len(n.Reports)-1]
				break
			}
			ids = append(ids, p.event)
		}
		if len(ids) > 0 && send(owner, n, ids) {
			o.pending = o.pending[len(ids):]
			o.last = now
		}
		o.due = time.Time{}
		if len(o.pending) > 0 {
			delay := q.config.MinInterval
			if delay < time.Millisecond {
				delay = time.Millisecond
			}
			o.due = now.Add(delay)
		}
	}
}
