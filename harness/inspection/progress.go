package inspection

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/work"
	"strings"
	"unicode/utf8"
)

const ProgressDefaultBytes = 16 * 1024
const ProgressMaxBytes = 32 * 1024

// ProgressQuery selects exactly one snapshot, immutable record or collection.
// Evidence resolution is supplied by the host's canonical execution reader.
type ProgressQuery struct {
	Mode        string                 `json:"mode"`
	WorkID      work.ID                `json:"work_id,omitempty"`
	ReportID    work.ProgressReportID  `json:"report_id,omitempty"`
	FindingID   work.ProgressFindingID `json:"finding_id,omitempty"`
	BriefID     work.ResearchBriefID   `json:"brief_id,omitempty"`
	EvidenceRef string                 `json:"evidence_ref,omitempty"`
	Cursor      string                 `json:"cursor,omitempty"`
	Limit       int                    `json:"limit,omitempty"`
	MaxBytes    int                    `json:"max_bytes,omitempty"`
}
type RecordFragment struct {
	Offset   int    `json:"offset"`
	Encoding string `json:"encoding"`
	Text     string `json:"text"`
	Complete bool   `json:"complete"`
}
type OversizedRecord struct {
	Index        int `json:"index"`
	EncodedBytes int `json:"encoded_bytes"`
}
type ProgressPage struct {
	Mode               string            `json:"mode"`
	WorkID             work.ID           `json:"work_id"`
	AssignedAtRevision work.Revision     `json:"assigned_at_revision"`
	Through            eventlog.Cursor   `json:"through"`
	Items              []json.RawMessage `json:"items,omitempty"`
	Oversized          *OversizedRecord  `json:"oversized,omitempty"`
	Fragment           *RecordFragment   `json:"fragment,omitempty"`
	NextCursor         string            `json:"next_cursor,omitempty"`
}
type progressCursor struct {
	Session string           `json:"session"`
	Prefix  uint64           `json:"prefix"`
	Actor   identity.ActorID `json:"actor"`
	Mode    string           `json:"mode"`
	WorkID  work.ID          `json:"work_id"`
	Record  string           `json:"record,omitempty"`
	Budget  int              `json:"budget"`
	Limit   int              `json:"limit"`
	Index   int              `json:"index"`
	Offset  int              `json:"offset"`
	Chunk   bool             `json:"chunk"`
}

// ProgressReader owns cursor integrity, not access authority. Archive hosts may
// use it directly; live hosts supply a current-work authorization callback.
type ProgressReader struct {
	Reader    *Reader
	key       [32]byte
	Authorize func(context.Context, identity.ActorID, work.ID) error
	Evidence  func(context.Context, *View, identity.ActorID, string) (work.ID, json.RawMessage, error)
}

func NewProgressReader(r *Reader) (*ProgressReader, error) {
	if r == nil {
		return nil, fmt.Errorf("reader required")
	}
	p := &ProgressReader{Reader: r}
	if _, err := rand.Read(p.key[:]); err != nil {
		return nil, err
	}
	return p, nil
}
func (p *ProgressReader) encode(c progressCursor) string {
	b, _ := json.Marshal(c)
	m := hmac.New(sha256.New, p.key[:])
	m.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
func (p *ProgressReader) decode(raw string) (progressCursor, error) {
	var c progressCursor
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || len(raw) > 4096 {
		return c, work.ErrInvalid
	}
	b, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return c, work.ErrInvalid
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return c, work.ErrInvalid
	}
	mac := hmac.New(sha256.New, p.key[:])
	mac.Write(b)
	if !hmac.Equal(sig, mac.Sum(nil)) || json.Unmarshal(b, &c) != nil {
		return c, work.ErrInvalid
	}
	return c, nil
}
func (p *ProgressReader) initial(ctx context.Context, actor identity.ActorID, q ProgressQuery) (progressCursor, error) {
	if q.Mode == "continue" {
		if q.Cursor == "" || q.WorkID != "" || q.ReportID != "" || q.FindingID != "" || q.BriefID != "" || q.EvidenceRef != "" || q.Limit != 0 || q.MaxBytes != 0 {
			return progressCursor{}, work.ErrInvalid
		}
		c, e := p.decode(q.Cursor)
		if e != nil {
			return c, e
		}
		if c.Actor != actor || c.Session != p.Reader.session {
			return c, work.ErrInvalid
		}
		return c, nil
	}
	if actor == "" || q.Cursor != "" {
		return progressCursor{}, work.ErrInvalid
	}
	budget := q.MaxBytes
	if budget == 0 {
		budget = ProgressDefaultBytes
	}
	if budget < 2048 || budget > ProgressMaxBytes || q.Limit < 0 || q.Limit > 100 {
		return progressCursor{}, work.ErrInvalid
	}
	limit := q.Limit
	if limit == 0 {
		limit = 20
	}
	selectors := 0
	for _, s := range []string{string(q.WorkID), string(q.ReportID), string(q.FindingID), string(q.BriefID), q.EvidenceRef} {
		if s != "" {
			selectors++
		}
	}
	if selectors != 1 {
		return progressCursor{}, work.ErrInvalid
	}
	c := progressCursor{Actor: actor, Mode: q.Mode, WorkID: q.WorkID, Budget: budget, Limit: limit}
	switch q.Mode {
	case "current", "reports", "findings":
		if q.WorkID == "" {
			return c, work.ErrInvalid
		}
	case "report":
		c.Record = string(q.ReportID)
	case "finding":
		c.Record = string(q.FindingID)
	case "brief":
		c.Record = string(q.BriefID)
	case "evidence":
		c.Record = q.EvidenceRef
	default:
		return c, work.ErrInvalid
	}
	if c.WorkID == "" && c.Record == "" {
		return c, work.ErrInvalid
	}
	if q.Limit != 0 && q.Mode != "reports" && q.Mode != "findings" {
		return c, work.ErrInvalid
	}
	head, err := p.Reader.Head(ctx)
	c.Session, c.Prefix = head.Cursor.Session, head.Cursor.Sequence
	return c, err
}
func rawRecord(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
func (p *ProgressReader) records(ctx context.Context, c *progressCursor) ([]json.RawMessage, work.Revision, error) {
	v, err := p.Reader.At(ctx, eventlog.Cursor{Session: c.Session, Sequence: c.Prefix})
	if err != nil {
		return nil, 0, err
	}
	var items []json.RawMessage
	switch c.Mode {
	case "current":
		x, e := v.GetWorkProgress(ctx, c.Actor, c.WorkID)
		if e != nil {
			return nil, 0, e
		}
		if len(x.Findings) > c.Limit {
			x.Findings = x.Findings[:c.Limit]
		}
		items = []json.RawMessage{rawRecord(x)}
	case "report":
		x, e := v.GetWorkProgressReport(ctx, c.Actor, work.ProgressReportID(c.Record))
		if e != nil {
			return nil, 0, e
		}
		c.WorkID = x.WorkID
		items = []json.RawMessage{rawRecord(x)}
	case "finding":
		x, e := v.GetProgressFinding(ctx, c.Actor, work.ProgressFindingID(c.Record))
		if e != nil {
			return nil, 0, e
		}
		c.WorkID = x.WorkID
		items = []json.RawMessage{rawRecord(x)}
	case "brief":
		x, e := v.GetResearchBrief(ctx, c.Actor, work.ResearchBriefID(c.Record))
		if e != nil {
			return nil, 0, e
		}
		c.WorkID = x.WorkID
		items = []json.RawMessage{rawRecord(x)}
	case "reports":
		m, _, e := v.workModel(ctx)
		if e != nil {
			return nil, 0, e
		}
		rs, e := m.ProgressReports(c.Actor, c.WorkID)
		if e != nil {
			return nil, 0, e
		}
		for _, r := range rs {
			items = append(items, rawRecord(r))
		}
	case "findings":
		x, e := v.GetWorkProgress(ctx, c.Actor, c.WorkID)
		if e != nil {
			return nil, 0, e
		}
		for _, f := range x.Findings {
			items = append(items, rawRecord(f))
		}
	case "evidence":
		if p.Evidence == nil {
			return nil, 0, work.ErrNotFound
		}
		id, x, e := p.Evidence(ctx, v, c.Actor, c.Record)
		if e != nil {
			return nil, 0, e
		}
		c.WorkID = id
		items = []json.RawMessage{x}
	default:
		return nil, 0, work.ErrInvalid
	}
	if p.Authorize != nil {
		if err := p.Authorize(ctx, c.Actor, c.WorkID); err != nil {
			return nil, 0, err
		}
	}
	w, err := v.InspectWork(ctx, c.Actor, c.WorkID)
	return items, w.Work.AssignedAtRevision, err
}
func fits(p ProgressPage, budget int) bool {
	b, e := json.Marshal(p)
	return e == nil && len(b) <= budget
}
func (p *ProgressReader) next(ctx context.Context, c progressCursor, total int) string {
	if c.Index < total {
		return p.encode(c)
	}
	if c.Mode == "current" {
		v, e := p.Reader.At(ctx, eventlog.Cursor{Session: c.Session, Sequence: c.Prefix})
		if e == nil {
			x, e := v.GetWorkProgress(ctx, c.Actor, c.WorkID)
			if e == nil && len(x.Findings) > c.Limit {
				c.Mode, c.Index, c.Offset, c.Chunk = "findings", c.Limit, 0, false
				return p.encode(c)
			}
		}
	}
	return ""
}
func (p *ProgressReader) Read(ctx context.Context, actor identity.ActorID, q ProgressQuery) (ProgressPage, error) {
	c, err := p.initial(ctx, actor, q)
	if err != nil {
		return ProgressPage{}, err
	}
	items, binding, err := p.records(ctx, &c)
	if err != nil {
		return ProgressPage{}, err
	}
	out := ProgressPage{Mode: c.Mode, WorkID: c.WorkID, AssignedAtRevision: binding, Through: eventlog.Cursor{Session: c.Session, Sequence: c.Prefix}}
	if c.Index < 0 || c.Index > len(items) || c.Offset < 0 {
		return out, work.ErrInvalid
	}
	if c.Chunk {
		if c.Index >= len(items) || c.Offset >= len(items[c.Index]) {
			return out, work.ErrInvalid
		}
		data := items[c.Index]
		low, high, best := 1, len(data)-c.Offset, 0
		for low <= high {
			n := (low + high) / 2
			end := c.Offset + n
			for end < len(data) && !utf8.RuneStart(data[end]) {
				end--
			}
			candidate := out
			next := c
			next.Offset = end
			complete := end == len(data)
			if complete {
				next.Index++
				next.Offset = 0
				next.Chunk = false
			}
			candidate.Fragment = &RecordFragment{Offset: c.Offset, Encoding: "utf-8-json", Text: string(data[c.Offset:end]), Complete: complete}
			candidate.NextCursor = p.next(ctx, next, len(items))
			if end > c.Offset && fits(candidate, c.Budget) {
				best = end - c.Offset
				low = n + 1
			} else {
				high = n - 1
			}
		}
		if best == 0 {
			return out, fmt.Errorf("%w: response budget cannot hold fragment", work.ErrInvalid)
		}
		end := c.Offset + best
		out.Fragment = &RecordFragment{Offset: c.Offset, Encoding: "utf-8-json", Text: string(data[c.Offset:end]), Complete: end == len(data)}
		c.Offset = end
		if end == len(data) {
			c.Index++
			c.Offset = 0
			c.Chunk = false
		}
		out.NextCursor = p.next(ctx, c, len(items))
		return out, nil
	}
	for c.Index < len(items) && len(out.Items) < c.Limit {
		candidate := out
		candidate.Items = append(append([]json.RawMessage(nil), out.Items...), items[c.Index])
		next := c
		next.Index++
		candidate.NextCursor = p.next(ctx, next, len(items))
		if !fits(candidate, c.Budget) {
			if len(out.Items) > 0 {
				out.NextCursor = p.encode(c)
				break
			}
			out.Oversized = &OversizedRecord{Index: c.Index, EncodedBytes: len(items[c.Index])}
			c.Chunk = true
			out.NextCursor = p.encode(c)
			break
		}
		out = candidate
		c = next
	}
	if len(out.Items) == 0 && out.Oversized == nil {
		out.NextCursor = p.next(ctx, c, len(items))
	}
	if !fits(out, c.Budget) {
		return ProgressPage{}, fmt.Errorf("%w: response envelope exceeds budget", work.ErrInvalid)
	}
	return out, nil
}

func (p *ProgressReader) collection(ctx context.Context, actor identity.ActorID, q work.ReportQuery, mode string) ([]json.RawMessage, string, error) {
	query := ProgressQuery{Mode: mode, WorkID: q.WorkID, Limit: q.Limit}
	if q.Cursor != "" {
		if q.WorkID != "" {
			return nil, "", work.ErrInvalid
		}
		query = ProgressQuery{Mode: "continue", Cursor: q.Cursor}
	}
	c, err := p.initial(ctx, actor, query)
	if err != nil {
		return nil, "", err
	}
	if c.Mode != mode || c.Chunk {
		return nil, "", work.ErrInvalid
	}
	if q.Limit != 0 {
		if q.Limit < 1 || q.Limit > 100 {
			return nil, "", work.ErrInvalid
		}
		c.Limit = q.Limit
	}
	items, _, err := p.records(ctx, &c)
	if err != nil {
		return nil, "", err
	}
	if c.Index < 0 || c.Index > len(items) {
		return nil, "", work.ErrInvalid
	}
	end := min(c.Index+c.Limit, len(items))
	page := items[c.Index:end]
	c.Index = end
	return page, p.next(ctx, c, len(items)), nil
}
func (p *ProgressReader) ListWorkProgressReports(ctx context.Context, actor identity.ActorID, q work.ReportQuery) (work.ReportPage, error) {
	raw, next, err := p.collection(ctx, actor, q, "reports")
	if err != nil {
		return work.ReportPage{}, err
	}
	out := work.ReportPage{Items: []work.WorkProgressReport{}, NextCursor: next}
	for _, b := range raw {
		var r work.WorkProgressReport
		if err = json.Unmarshal(b, &r); err != nil {
			return work.ReportPage{}, err
		}
		out.Items = append(out.Items, r)
	}
	return out, nil
}
func (p *ProgressReader) ListWorkProgressFindings(ctx context.Context, actor identity.ActorID, q work.ReportQuery) (work.ProgressFindingPage, error) {
	raw, next, err := p.collection(ctx, actor, q, "findings")
	if err != nil {
		return work.ProgressFindingPage{}, err
	}
	out := work.ProgressFindingPage{Items: []work.ProgressFinding{}, NextCursor: next}
	for _, b := range raw {
		var f work.ProgressFinding
		if err = json.Unmarshal(b, &f); err != nil {
			return work.ProgressFindingPage{}, err
		}
		out.Items = append(out.Items, f)
	}
	return out, nil
}
