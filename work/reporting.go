package work

import (
	"context"
	"errors"
)

// Change contains the complete values changed by one ledger mutation, including
// secondary work, plan, submission and audit updates needed by a replay view.
type Change struct {
	Works       []Work       `json:"works,omitempty"`
	Plans       []Plan       `json:"plans,omitempty"`
	Submissions []Submission `json:"submissions,omitempty"`
	Audits      []Audit      `json:"audits,omitempty"`
}

func (c Change) Clone() Change {
	v := Change{}
	for _, x := range c.Works {
		v.Works = append(v.Works, x.Clone())
	}
	for _, x := range c.Plans {
		v.Plans = append(v.Plans, x.Clone())
	}
	for _, x := range c.Submissions {
		v.Submissions = append(v.Submissions, x.Clone())
	}
	for _, x := range c.Audits {
		v.Audits = append(v.Audits, x.Clone())
	}
	return v
}

type Reporter interface {
	Publish(context.Context, Event) error
}
type ReporterFunc func(context.Context, Event) error

func (f ReporterFunc) Publish(ctx context.Context, e Event) error { return f(ctx, e) }

type Option func(*Store)

func WithReporter(r Reporter) Option { return func(s *Store) { s.reporter = r } }
func (s *Store) beginMutation() error {
	s.emission.Lock()
	s.mu.Lock()
	if s.failure != nil {
		err := s.failure
		s.mu.Unlock()
		s.emission.Unlock()
		return err
	}
	s.change = Change{}
	return nil
}
func (s *Store) endMutation(err *error) {
	start := s.visibleEvents
	if len(s.events) > start {
		c := s.change.Clone()
		s.events[len(s.events)-1].Change = &c
	}
	events := make([]Event, len(s.events)-start)
	for i, e := range s.events[start:] {
		events[i] = e.Clone()
	}
	s.mu.Unlock()
	var publishErr error
	if s.reporter != nil {
		for _, e := range events {
			if publishErr = s.reporter.Publish(context.Background(), e); publishErr != nil {
				break
			}
		}
	}
	s.mu.Lock()
	if publishErr != nil {
		s.failure = publishErr
	}
	if publishErr == nil {
		s.visibleEvents = len(s.events)
		if len(events) > 0 {
			select {
			case s.ready <- struct{}{}:
			default:
			}
		}
	}
	s.change = Change{}
	s.mu.Unlock()
	s.emission.Unlock()
	*err = errors.Join(*err, publishErr)
}
func (s *Store) putWork(id ID, v Work) {
	s.works[id] = v
	s.change.Works = append(s.change.Works, v.Clone())
}
func (s *Store) putPlan(id PlanID, v Plan) {
	s.plans[id] = v
	s.change.Plans = append(s.change.Plans, v.Clone())
}
func (s *Store) putSubmission(id SubmissionID, v Submission) {
	s.submissions[id] = v
	s.change.Submissions = append(s.change.Submissions, v.Clone())
}
func (s *Store) putAudit(id AuditID, v Audit) {
	s.audits[id] = v
	s.change.Audits = append(s.change.Audits, v.Clone())
}
