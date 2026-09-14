package workflow

import (
	"errors"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
	"time"
)

// ProgressNoticeWakes classifies only the host's structured ordinary notices.
// Current state retires a reporting binding even if dispatcher timers lag behind.
func ProgressNoticeWakes(m message.Message, get func(identity.ActorID, work.ID) (work.Work, error)) (bool, error) {
	if m.Kind == message.Observation {
		return false, nil
	}
	if m.Progress == nil || m.Progress.Attention || m.Event != nil || len(m.Progress.Briefs) > 0 || len(m.Progress.Covered) > 0 {
		return true, nil
	}
	for _, r := range m.Progress.Reports {
		w, err := get(m.To, r.WorkID)
		if errors.Is(err, work.ErrNotFound) || errors.Is(err, work.ErrForbidden) {
			continue
		}
		if err != nil {
			return false, err
		}
		if w.State == work.Active && w.AssignedAtRevision == r.AssignedAtRevision && w.Owner == m.To {
			return true, nil
		}
	}
	return false, nil
}

// coverageChange inspects all changed work, including closed repair/audit siblings.
func coverageChange(prior map[work.ID]work.Work, c *work.Change) []message.ProgressCoverage {
	var covered []message.ProgressCoverage
	if c == nil {
		return covered
	}
	for _, w := range c.Works {
		old, ok := prior[w.ID]
		if ok && old.State == work.Active && (w.State != work.Active || w.AssignedAtRevision != old.AssignedAtRevision) {
			covered = append(covered, message.ProgressCoverage{WorkID: w.ID, AssignedAtRevision: old.AssignedAtRevision, ThroughRevision: w.Revision})
		}
		prior[w.ID] = w.Clone()
	}
	return covered
}
func (q *noticeQueue) retire(get func(identity.ActorID, work.ID) (work.Work, error), ack func(work.EventID)) error {
	for owner, o := range q.owners {
		kept := o.pending[:0]
		for _, p := range o.pending {
			w, err := get(owner, p.ref.WorkID)
			if err != nil {
				return err
			}
			if w.State != work.Active || w.AssignedAtRevision != p.ref.AssignedAtRevision {
				ack(p.event)
			} else {
				kept = append(kept, p)
			}
		}
		o.pending = kept
		if len(kept) == 0 {
			o.due = time.Time{}
		}
	}
	return nil
}
