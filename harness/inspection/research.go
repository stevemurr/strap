package inspection

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/research"
	"github.com/stevemurr/strap/work"
)

// ResearchReader reuses signed fixed-prefix cursor mechanics, with its own key
// and mode namespace. It retains no report or source bodies outside the log.
type ResearchReader struct{ *ProgressReader }

func NewResearchReader(r *Reader) (*ResearchReader, error) {
	p, err := NewProgressReader(r)
	if err != nil {
		return nil, err
	}
	return &ResearchReader{p}, nil
}

type ResearchPage struct {
	Mode       string          `json:"mode"`
	WorkID     work.ID         `json:"work_id"`
	Through    eventlog.Cursor `json:"through"`
	Data       json.RawMessage `json:"data,omitempty"`
	Fragment   *RecordFragment `json:"fragment,omitempty"`
	TotalBytes int             `json:"total_bytes"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

func (v *View) ResearchRun(ctx context.Context, actor identity.ActorID, id string) (projection.ResearchView, error) {
	run, err := v.projection.Research(id)
	if err != nil {
		if hint, ok := work.Misrouted(id, "run-"); ok {
			return run, fmt.Errorf("%w: %s", err, hint)
		}
		return run, fmt.Errorf("%w: research run %s; list your runs with mode runs and your work_id", err, id)
	}
	if _, err = v.InspectWork(ctx, actor, work.ID(run.Binding.WorkID)); err != nil {
		return projection.ResearchView{}, err
	}
	if run.FinishedAt.IsZero() && v.reader.owned != nil {
		run.Status = "incomplete"
	}
	return run, nil
}
func (v *View) ResearchRuns(ctx context.Context, actor identity.ActorID, id work.ID) ([]projection.ResearchView, error) {
	if _, err := v.InspectWork(ctx, actor, id); err != nil {
		return nil, err
	}
	out := []projection.ResearchView{}
	for _, r := range v.projection.ResearchRuns() {
		if r.Binding.WorkID == string(id) {
			if r.FinishedAt.IsZero() && v.reader.owned != nil {
				r.Status = "incomplete"
			}
			out = append(out, r)
		}
	}
	return out, nil
}
func (v *View) researchRecord(ctx context.Context, cursor eventlog.Cursor) (research.Event, error) {
	raw, err := v.ResolveRecord(ctx, eventlog.Record{Session: cursor.Session, Sequence: cursor.Sequence})
	if err != nil {
		return research.Event{}, err
	}
	decoded, err := eventcodec.DecodeEvent(raw)
	if err != nil {
		return research.Event{}, err
	}
	e, ok := decoded.(conversation.ResearchEvent)
	if !ok {
		return research.Event{}, work.ErrInvalid
	}
	if err := e.Event.Validate(); err != nil {
		return research.Event{}, err
	}
	return e.Event, nil
}
func (v *View) ResearchReport(ctx context.Context, actor identity.ActorID, id string) (research.Report, error) {
	run, err := v.ResearchRun(ctx, actor, id)
	if err != nil {
		return research.Report{}, err
	}
	e, err := v.researchRecord(ctx, run.ReportRecord)
	if err != nil {
		return research.Report{}, err
	}
	if e.Report == nil || e.Report.ID != id {
		return research.Report{}, work.ErrInvalid
	}
	if run.Status == "incomplete" {
		e.Report.Status = "incomplete"
		e.Report.StopReason = "archive_without_finish"
	}
	return *e.Report, nil
}
func (v *View) ResearchSource(ctx context.Context, actor identity.ActorID, id, source string) (research.Source, error) {
	run, err := v.ResearchRun(ctx, actor, id)
	if err != nil {
		return research.Source{}, err
	}
	cursor, ok := run.SourceRecords[source]
	if !ok {
		return research.Source{}, projection.ErrNotFound
	}
	e, err := v.researchRecord(ctx, cursor)
	if err != nil {
		return research.Source{}, err
	}
	if e.Source == nil || e.RunID != id || e.Source.ID != source {
		return research.Source{}, work.ErrInvalid
	}
	return *e.Source, nil
}

func (p *ResearchReader) Read(ctx context.Context, actor identity.ActorID, q research.ReadQuery) (ResearchPage, error) {
	var c progressCursor
	if q.Mode == "continue" {
		if q.Cursor == "" || q.WorkID != "" || q.RunID != "" || q.SourceID != "" || q.MaxBytes != 0 {
			return ResearchPage{}, work.ErrInvalid
		}
		var err error
		c, err = p.decode(q.Cursor)
		if err != nil {
			return ResearchPage{}, err
		}
		if c.Actor != actor || c.Session != p.Reader.session || !strings.HasPrefix(c.Mode, "research/") {
			return ResearchPage{}, work.ErrInvalid
		}
	} else {
		if actor == "" || q.Cursor != "" {
			return ResearchPage{}, work.ErrInvalid
		}
		budget := q.MaxBytes
		if budget == 0 {
			budget = 16 << 10
		}
		if budget < 2048 || budget > 32<<10 {
			return ResearchPage{}, work.ErrInvalid
		}
		if q.Mode == "runs" {
			if q.WorkID == "" || q.RunID != "" || q.SourceID != "" {
				return ResearchPage{}, work.ErrInvalid
			}
		} else if q.Mode == "run" || q.Mode == "sources" || q.Mode == "source" {
			if q.WorkID != "" || q.RunID == "" || q.Mode == "source" && q.SourceID == "" || q.Mode != "source" && q.SourceID != "" {
				return ResearchPage{}, work.ErrInvalid
			}
		} else {
			return ResearchPage{}, work.ErrInvalid
		}
		h, err := p.Reader.Head(ctx)
		if err != nil {
			return ResearchPage{}, err
		}
		through := h.Cursor
		if p.Through != (eventlog.Cursor{}) {
			through = p.Through
		}
		c = progressCursor{Session: through.Session, Prefix: through.Sequence, Actor: actor, Mode: "research/" + q.Mode, WorkID: work.ID(q.WorkID), Record: q.RunID, Budget: budget}
		if q.Mode == "source" {
			c.Record += "/" + q.SourceID
		}
	}
	view, err := p.Reader.At(ctx, eventlog.Cursor{Session: c.Session, Sequence: c.Prefix})
	if err != nil {
		return ResearchPage{}, err
	}
	mode := strings.TrimPrefix(c.Mode, "research/")
	id, source, _ := strings.Cut(c.Record, "/")
	if mode != "runs" {
		run, err := view.ResearchRun(ctx, actor, id)
		if err != nil {
			return ResearchPage{}, err
		}
		c.WorkID = work.ID(run.Binding.WorkID)
	}
	if p.Authorize != nil {
		if err := p.Authorize(ctx, actor, c.WorkID); err != nil {
			return ResearchPage{}, err
		}
	}
	var value any
	switch mode {
	case "runs":
		value, err = view.ResearchRuns(ctx, actor, c.WorkID)
	case "run":
		value, err = view.ResearchReport(ctx, actor, id)
	case "source":
		value, err = view.ResearchSource(ctx, actor, id, source)
	case "sources":
		var run projection.ResearchView
		run, err = view.ResearchRun(ctx, actor, id)
		if err != nil {
			break
		}
		ids := []string{}
		for id := range run.SourceRecords {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		list := []research.Source{}
		for _, sid := range ids {
			var s research.Source
			s, err = view.ResearchSource(ctx, actor, id, sid)
			if err != nil {
				break
			}
			s.Text = ""
			list = append(list, s)
		}
		value = list
	default:
		return ResearchPage{}, work.ErrInvalid
	}
	if err != nil {
		return ResearchPage{}, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ResearchPage{}, err
	}
	if c.Offset < 0 || c.Offset > len(raw) {
		return ResearchPage{}, work.ErrInvalid
	}
	page := ResearchPage{Mode: mode, WorkID: c.WorkID, Through: view.Through(), TotalBytes: len(raw)}
	if c.Offset == 0 {
		page.Data = raw
		encoded, _ := json.Marshal(page)
		if len(encoded) <= c.Budget {
			return page, nil
		}
		page.Data = nil
	}
	n := min(len(raw)-c.Offset, c.Budget-1024)
	for n > 0 {
		for c.Offset+n < len(raw) && !utf8.RuneStart(raw[c.Offset+n]) {
			n--
		}
		next := c
		next.Offset += n
		page.Fragment = &RecordFragment{Offset: c.Offset, Encoding: "json", Text: string(raw[c.Offset:next.Offset]), Complete: next.Offset == len(raw)}
		if next.Offset < len(raw) {
			page.NextCursor = p.encode(next)
		} else {
			page.NextCursor = ""
		}
		encoded, _ := json.Marshal(page)
		if len(encoded) <= c.Budget {
			return page, nil
		}
		n /= 2
	}
	return ResearchPage{}, fmt.Errorf("%w: research page budget too small", work.ErrInvalid)
}

func ResearchQueryFromValues(v url.Values) (research.ReadQuery, error) {
	q := research.ReadQuery{Mode: v.Get("mode"), WorkID: v.Get("work_id"), RunID: v.Get("run_id"), SourceID: v.Get("source_id"), Cursor: v.Get("cursor")}
	for k, x := range v {
		if len(x) != 1 || x[0] == "" {
			return q, work.ErrInvalid
		}
		switch k {
		case "actor", "mode", "work_id", "run_id", "source_id", "cursor", "max_bytes":
		default:
			return q, work.ErrInvalid
		}
	}
	if v.Has("max_bytes") {
		n, err := strconv.Atoi(v.Get("max_bytes"))
		if err != nil {
			return q, work.ErrInvalid
		}
		q.MaxBytes = n
	}
	return q, nil
}
