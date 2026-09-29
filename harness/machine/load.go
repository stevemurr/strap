package machine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/harness/record"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/roster"
)

// ReadTrace calls fn with every record of a trace.jsonl archive in order.
// Framed records have their content resolved first; e is the decoded domain
// event, or nil for log control records and records with no domain event.
func ReadTrace(ctx context.Context, path string, fn func(r eventlog.Record, e conversation.Event) error) error {
	log, err := eventlog.OpenJSONL(ctx, path)
	if err != nil {
		return err
	}
	defer log.Close(context.Background())
	var view *inspection.View
	var reader *inspection.Reader
	defer func() {
		if reader != nil {
			_ = reader.Close(context.Background())
		}
	}()
	resolve := func(r eventlog.Record) (eventlog.Record, error) {
		if _, framed := record.Frame(r.Payload); !framed {
			return r, nil
		}
		if view == nil {
			if reader, err = inspection.OpenJSONL(ctx, path); err != nil {
				return r, err
			}
			if view, err = reader.At(ctx, eventlog.Cursor{}); err != nil {
				return r, err
			}
		}
		return view.ResolveRecord(ctx, r)
	}
	for after := uint64(0); ; {
		page, err := log.Read(ctx, eventlog.Query{After: after, Limit: 1000})
		if err != nil {
			return err
		}
		for _, r := range page.Events {
			after = r.Sequence
			if r, err = resolve(r); err != nil {
				return fmt.Errorf("resolve record %d %s: %w", r.Sequence, r.Kind, err)
			}
			e, err := eventcodec.DecodeEvent(r)
			if err != nil {
				e = nil // Not a conversation event, such as session_configured.
			}
			if err := fn(r, e); err != nil {
				return err
			}
		}
		if len(page.Events) == 0 || after >= page.Latest {
			return nil
		}
	}
}

// Load rebuilds a session's timeline from its recorded trace, timing each
// transition by the record's own timestamp. User messages take intents in the
// order they were sent; any beyond them are Unknown.
func Load(ctx context.Context, path string, intents []Intent) (Timeline, error) {
	var r *Recorder
	var start time.Time
	var dir string
	err := ReadTrace(ctx, path, func(rec eventlog.Record, e conversation.Event) error {
		if start.IsZero() {
			start = rec.Time
		}
		if rec.Kind == "session_configured" {
			var c struct {
				Dir string `json:"dir"`
			}
			_ = json.Unmarshal(rec.Payload, &c)
			dir = c.Dir
			if r != nil {
				r.Dir = dir
			}
		}
		if reg, ok := e.(conversation.AgentRegistered); ok && (reg.Registration.Role == roster.Manager || reg.Registration.Role == roster.Agent) && r == nil {
			r = NewRecorder(reg.Registration.AgentID)
			r.Dir = dir
		}
		if r == nil || e == nil {
			return nil
		}
		at := rec.Time.Sub(start)
		if msg, ok := e.(conversation.MessageEvent); ok && msg.Message.From == message.User && msg.Message.To == r.Manager {
			kind := Unknown
			if n := len(r.Turns); n < len(intents) {
				kind = intents[n]
			}
			r.Turn(at, msg.Message.Content, kind)
		}
		r.Observe(at, e)
		return nil
	})
	if err != nil {
		return Timeline{}, err
	}
	if r == nil {
		return Timeline{}, errors.New("trace has no manager registration")
	}
	return r.Timeline, nil
}
