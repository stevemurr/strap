package eventlog

import (
	"context"
	"errors"
)

// Memory retains every accepted record. Limits are quotas, never eviction rules.
// Zero Limits means unlimited retained history; publication is bounded separately.
type Memory struct {
	*storeState
	limits Limits
	events []Event
	bytes  int
}

func NewMemory(session string, limits Limits) (*Memory, error) {
	if session == "" {
		return nil, errors.New("session identity is required")
	}
	if limits != (Limits{}) {
		if err := limits.Validate(); err != nil {
			return nil, err
		}
	}
	return &Memory{storeState: newState(session), limits: limits}, nil
}
func (m *Memory) Append(ctx context.Context, d Data) (Event, error) {
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	if err := d.Validate(); err != nil {
		return Event{}, err
	}
	if d.Size() > MaxRecordBytes {
		return Event{}, ErrRecordSize
	}
	d = d.Clone()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return Event{}, err
	}
	return m.append(d)
}
func (m *Memory) append(d Data) (Event, error) {
	if m.limits.Entries > 0 && len(m.events) >= m.limits.Entries || m.limits.Bytes > 0 && m.bytes+d.Size() > m.limits.Bytes {
		return Event{}, ErrQuota
	}
	m.latest++
	e := Event{Schema: SchemaVersion, Session: m.session, Sequence: m.latest, Data: d}
	m.events = append(m.events, e)
	m.bytes += e.Size()
	m.signal()
	return e.Clone(), nil
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
	p, err := m.page(q)
	if err != nil {
		return p, err
	}
	bytes := 0
	for _, e := range m.events[int(q.After):] {
		stop, err := overBudget(p, e, bytes, q)
		if err != nil {
			return p, err
		}
		if stop {
			break
		}
		p.Events = append(p.Events, e.Clone())
		p.Next = e.Sequence
		bytes += e.Size()
		if len(p.Events) >= q.Limit {
			break
		}
	}
	return p, nil
}
func (m *Memory) Seal(ctx context.Context, o Outcome) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d := terminal(o)
	if d.Size() > MaxRecordBytes {
		return ErrRecordSize
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.outcome != nil {
		if *m.outcome != o {
			return errors.New("conflicting shutdown outcome")
		}
		return nil
	}
	if err := m.writable(); err != nil {
		return err
	}
	if _, err := m.append(d); err != nil {
		return err
	}
	m.outcome = &o
	return nil
}
func (m *Memory) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.disposed {
		m.disposed = true
		m.events = nil
		m.bytes = 0
		m.signal()
	}
	return nil
}
