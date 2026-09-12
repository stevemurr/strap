package eventlog

import (
	"context"
	"errors"
	"sync"
)

type Memory struct {
	mu       sync.Mutex
	session  string
	limits   Limits
	events   []Event
	bytes    int
	latest   uint64
	outcome  *Outcome
	disposed bool
}

func NewMemory(session string, limits Limits) (*Memory, error) {
	if session == "" {
		return nil, errors.New("session identity is required")
	}
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	return &Memory{session: session, limits: limits}, nil
}
func (m *Memory) Append(ctx context.Context, d Data) (Event, error) {
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	if err := d.Validate(); err != nil {
		return Event{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.disposed {
		return Event{}, ErrDisposed
	}
	if m.outcome != nil {
		return Event{}, ErrSealed
	}
	return m.append(d), nil
}
func (m *Memory) append(d Data) Event {
	d, _ = Fit(d, m.limits.Bytes)
	m.latest++
	e := Event{Schema: SchemaVersion, Session: m.session, Sequence: m.latest, Data: d.Clone()}
	for len(m.events) > 0 && (len(m.events) >= m.limits.Entries || m.bytes+e.Size() > m.limits.Bytes) {
		m.bytes -= m.events[0].Size()
		copy(m.events, m.events[1:])
		m.events[len(m.events)-1] = Event{}
		m.events = m.events[:len(m.events)-1]
	}
	m.events = append(m.events, e)
	m.bytes += e.Size()
	return e.Clone()
}
func (m *Memory) Read(ctx context.Context, q Query) (Page, error) {
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	if err := q.Validate(); err != nil {
		return Page{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.disposed {
		return Page{}, ErrDisposed
	}
	p := Page{Next: q.After, Latest: m.latest, Sealed: m.outcome != nil}
	if m.outcome != nil {
		o := *m.outcome
		p.Outcome = &o
	}
	if len(m.events) > 0 {
		p.Earliest = m.events[0].Sequence
	}
	if err := cursor(q, p.Earliest, p.Latest); err != nil {
		return p, err
	}
	for _, e := range m.events {
		if e.Sequence > q.After {
			p.Events = append(p.Events, e.Clone())
			p.Next = e.Sequence
			if len(p.Events) == q.Limit {
				break
			}
		}
	}
	return p, nil
}
func (m *Memory) Seal(ctx context.Context, o Outcome) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.disposed {
		return ErrDisposed
	}
	if m.outcome != nil {
		if *m.outcome != o {
			return errors.New("conflicting shutdown outcome")
		}
		return nil
	}
	m.append(terminal(o))
	m.outcome = &o
	return nil
}
func (m *Memory) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disposed = true
	m.events = nil
	m.bytes = 0
	return nil
}
