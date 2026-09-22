package projection

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"time"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/research"
	"github.com/stevemurr/strap/roster"
)

type ResearchView struct {
	ID            string                     `json:"report_id"`
	Binding       research.Binding           `json:"binding"`
	Sequence      uint64                     `json:"sequence"`
	StartedAt     time.Time                  `json:"started_at"`
	FinishedAt    time.Time                  `json:"finished_at"`
	Deadline      time.Time                  `json:"deadline"`
	Stage         string                     `json:"stage"`
	Completed     uint64                     `json:"completed_steps"`
	Status        string                     `json:"status"`
	Spend         research.Spend             `json:"spend"`
	ReportRecord  eventlog.Cursor            `json:"report_record"`
	SourceRecords map[string]eventlog.Cursor `json:"source_records"`
	Through       eventlog.Cursor            `json:"through"`
}

func (p *Projector) applyResearch(rec eventlog.Record, framed bool) (func(), error) {
	if rec.Schema < 6 {
		return nil, errors.New("research record requires schema 6")
	}
	var wrapper conversation.ResearchEvent
	if err := json.Unmarshal(rec.Payload, &wrapper); err != nil {
		return nil, err
	}
	e := wrapper.Event
	if !framed {
		if err := e.Validate(); err != nil {
			return nil, err
		}
	} else {
		// Framing has already verified the complete body hash. Source content is
		// validated when read; metadata suffices for the lifetime projection.
		if err := e.ValidateControl(); err != nil {
			return nil, err
		}
	}
	if e.RunID != rec.Correlation || e.Binding.Actor != rec.Agent || e.Binding.WorkID == "" || e.Binding.Assignment == 0 || e.At.IsZero() {
		return nil, errors.New("research record binding mismatch")
	}
	key := fmt.Sprintf("%s/%d", e.Binding.WorkID, e.Binding.Assignment)
	if p.bindings[key] != identity.ActorID(e.Binding.Actor) || p.registrations[identity.ActorID(e.Binding.Actor)].Role != roster.Researcher {
		return nil, errors.New("research without accepted assignment")
	}
	v, exists := p.researchRuns[e.RunID]
	if e.Kind == "started" {
		if exists || e.Sequence != 1 {
			return nil, errors.New("duplicate research start")
		}
		invocation, ok := p.toolStates[e.Binding.InvocationID]
		if !ok || invocation.Finished || invocation.Agent != identity.ActorID(e.Binding.Actor) {
			return nil, errors.New("research requires active tool invocation")
		}
		v = ResearchView{ID: e.RunID, Binding: e.Binding, StartedAt: e.At, Status: "running", SourceRecords: map[string]eventlog.Cursor{}}
	} else if !exists || !v.FinishedAt.IsZero() || e.Sequence != v.Sequence+1 || e.Binding != v.Binding || e.Completed < v.Completed {
		return nil, errors.New("research record sequence or lifecycle mismatch")
	}
	v.SourceRecords = maps.Clone(v.SourceRecords)
	v.Sequence = e.Sequence
	v.Stage = e.Stage
	v.Completed = e.Completed
	v.Deadline = e.Deadline
	v.Through = rec.Cursor()
	if e.Source != nil {
		if _, exists := v.SourceRecords[e.Source.ID]; exists {
			return nil, errors.New("duplicate research source")
		}
		v.SourceRecords[e.Source.ID] = rec.Cursor()
	}
	if e.Report != nil {
		v.ReportRecord = rec.Cursor()
		v.Spend = e.Report.Spend
		if e.Kind == "finished" {
			v.FinishedAt = e.Report.FinishedAt
			v.Status = e.Report.Status
		}
	}
	if e.Spend != nil {
		v.Spend = *e.Spend
	}
	return func() { p.researchRuns[e.RunID] = v }, nil
}
func (p *Projector) Research(id string) (ResearchView, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	v, ok := p.researchRuns[id]
	if !ok {
		return ResearchView{}, ErrNotFound
	}
	v.SourceRecords = maps.Clone(v.SourceRecords)
	return v, nil
}
func (p *Projector) ResearchRuns() []ResearchView {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := []ResearchView{}
	for _, v := range p.researchRuns {
		v.SourceRecords = maps.Clone(v.SourceRecords)
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}
