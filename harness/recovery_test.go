package harness_test

import (
	"context"
	"errors"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/harness/record"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type streamingModel struct{ ready, release chan struct{} }

func (p *streamingModel) Submit(ctx context.Context, _ provider.Request, o provider.Observer) (provider.Response, error) {
	if err := o.OnDelta(provider.Delta{Text: "hello 🌎"}); err != nil {
		return provider.Response{}, err
	}
	close(p.ready)
	select {
	case <-p.release:
		return provider.Response{Content: "hello 🌎!"}, nil
	case <-ctx.Done():
		return provider.Response{}, ctx.Err()
	}
}
func TestReplayRecoversActiveOutputAndFixedTextPrefix(t *testing.T) {
	p := &streamingModel{ready: make(chan struct{}), release: make(chan struct{})}
	s := newLifecycleSession(t, context.Background(), p)
	if _, err := s.Send(s.Root(), "go"); err != nil {
		t.Fatal(err)
	}
	<-p.ready
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	id := identity.OutputID{Agent: s.Root(), Call: 1}
	var inspection harness.OutputInspection
	for {
		var err error
		inspection, err = s.InspectOutput(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if inspection.Output.TextBytes > 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if inspection.Output.Status != agent.OutputActive {
		t.Fatal(inspection)
	}
	prefix := inspection.Output.Through
	a := projection.New(identity.SessionID(s.ID()))
	b := projection.New(identity.SessionID(s.ID()))
	records, err := s.Events(ctx, eventlog.Query{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range records.Events {
		if e.Sequence > prefix.Sequence {
			break
		}
		if err := a.Apply(e); err != nil {
			t.Fatal(e.Kind, err)
		}
		if err := b.Apply(e); err != nil {
			t.Fatal(err)
		}
	}
	av, _ := a.Output(id)
	bv, _ := b.Output(id)
	if av.TextBytes != bv.TextBytes || av.Status != agent.OutputActive {
		t.Fatal(av, bv)
	}
	close(p.release)
	text, err := s.ReadOutputText(ctx, harness.OutputTextQuery{Output: id, Through: prefix, MaxBytes: 7})
	if err != nil || text.Text != "hello " || text.End {
		t.Fatal(text, err)
	}
	rest, err := s.ReadOutputText(ctx, harness.OutputTextQuery{Output: id, Through: prefix, Offset: text.Next, MaxBytes: 4})
	if err != nil || rest.Text != "🌎" || !rest.End {
		t.Fatal(rest, err)
	}
	if _, err = s.ReadOutputText(ctx, harness.OutputTextQuery{Output: id, Through: prefix, Offset: 7, MaxBytes: 10}); err == nil {
		t.Fatal("invalid UTF-8 offset accepted")
	}
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestLargeMessageFramingRetainsCompleteContent(t *testing.T) {
	s := newLifecycleSession(t, context.Background(), idle{})
	body := strings.Repeat("large ✓ ", 50000)
	if _, err := s.Send(s.Root(), body); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	var after uint64
	var reference *eventlog.ContentRef
	p := projection.New(identity.SessionID(s.ID()))
	for {
		page, err := s.Events(context.Background(), eventlog.Query{After: after, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Events {
			if e.Size() > eventlog.MaxRecordBytes {
				t.Fatal("oversize record")
			}
			if err := p.Apply(e); err != nil {
				t.Fatal(e.Kind, err)
			}
			if e.Kind == "message" {
				if frame, ok := record.Frame(e.Payload); ok {
					reference = frame.Content
					resolved, err := s.ResolveRecord(context.Background(), e)
					if err != nil || !strings.Contains(string(resolved.Payload), body) {
						t.Fatal("message did not round trip", err)
					}
				}
			}
		}
		after = page.Next
		if after == page.Latest {
			break
		}
	}
	if reference == nil {
		t.Fatal("no framed message")
	}
	page, err := s.ReadContent(context.Background(), harness.ContentQuery{ID: reference.ID, MaxBytes: 31})
	if err != nil || len(page.Data) != 31 || page.End {
		t.Fatal(page, err)
	}
	if _, err = s.ReadContent(context.Background(), harness.ContentQuery{ID: "missing", MaxBytes: 31}); !errors.Is(err, projection.ErrNotFound) {
		t.Fatal(err)
	}
}

type missingFinishStore struct{ eventlog.Store }

func (s missingFinishStore) Append(ctx context.Context, d eventlog.Data) (eventlog.Event, error) {
	if d.Kind == "output_finished" {
		return eventlog.Event{}, errors.New("finish write failed")
	}
	return s.Store.Append(ctx, d)
}

type simpleResponse struct{}

func (simpleResponse) Submit(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
	return provider.Response{Content: "committed text"}, nil
}
func TestHistoryCommitSurvivesMissingOutputFinish(t *testing.T) {
	cfg := harness.DefaultConfig()
	cfg.Web = nil
	cfg.LocalTools = false
	failed := make(chan struct{})
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: simpleResponse{}, CaptureFailure: func(error) { close(failed) }, EventStore: func(id string) (eventlog.Store, error) {
		m, e := eventlog.NewMemory(id, eventlog.Limits{})
		return missingFinishStore{m}, e
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	if _, err = s.Send(s.Root(), "go"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-failed:
	case <-time.After(time.Second):
		t.Fatal("missing capture failure")
	}
	v, err := s.InspectOutput(context.Background(), identity.OutputID{Agent: s.Root(), Call: 1})
	if err != nil || v.Source.State != eventlog.Failed || v.Output.Status != agent.OutputActive {
		t.Fatal(v, err)
	}
	info, err := s.InspectAgent(s.Root(), conversation.InspectOptions{Transcript: &agent.TranscriptQuery{Limit: 100}})
	if err != nil || len(info.Transcript.Entries) != 3 || info.Transcript.Entries[2].Message.Content.Text() != "committed text" {
		t.Fatal(info, err)
	}
}

type textResponse string

func (p textResponse) Submit(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
	return provider.Response{Content: string(p)}, nil
}
func TestEscapedOutputStreamsWithinSmallQueueBudget(t *testing.T) {
	cfg := harness.DefaultConfig()
	cfg.Web = nil
	cfg.LocalTools = false
	cfg.Events.Queue = eventlog.Limits{Entries: 1, Bytes: 4096}
	text := strings.Repeat("\x01🌎", 4000)
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: textResponse(text)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	if _, err = s.Send(s.Root(), "go"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	id := identity.OutputID{Agent: s.Root(), Call: 1}
	sub, err := s.Subscribe(ctx, harness.SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	for {
		e, err := sub.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if e.Kind == "output_finished" {
			break
		}
	}
	view, err := s.InspectOutput(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ReadOutputText(ctx, harness.OutputTextQuery{Output: id, Through: view.Output.Through, MaxBytes: 1 << 20})
	if err != nil || page.Text != text || !page.End {
		t.Fatal("escaped output mismatch", len(page.Text), err)
	}
}

func TestDiskReplayMatchesClosedSessionProjection(t *testing.T) {
	cfg := harness.DefaultConfig()
	cfg.Web = nil
	cfg.LocalTools = false
	cfg.Events.JSONLPath = filepath.Join(t.TempDir(), "session.jsonl")
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: simpleResponse{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	sub, err := s.Subscribe(ctx, harness.SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if _, err = s.Send(s.Root(), strings.Repeat("large input ✓ ", 12000)); err != nil {
		t.Fatal(err)
	}
	live := projection.New(identity.SessionID(s.ID()))
	for {
		e, err := sub.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = live.Apply(e); err != nil {
			t.Fatal(err)
		}
		if e.Kind == "output_finished" {
			break
		}
	}
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	for {
		e, err := sub.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = live.Apply(e); err != nil {
			t.Fatal(err)
		}
	}
	archive, err := eventlog.OpenJSONL(ctx, cfg.Events.JSONLPath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close(ctx)
	fresh := projection.New(identity.SessionID(s.ID()))
	var after uint64
	for {
		page, err := archive.Read(ctx, eventlog.Query{After: after, Limit: 7, MaxBytes: 1 << 20})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Events {
			if err = fresh.Apply(e); err != nil {
				t.Fatal(err)
			}
		}
		after = page.Next
		if after == page.Latest {
			break
		}
	}
	id := identity.OutputID{Agent: s.Root(), Call: 1}
	a, _ := live.Output(id)
	b, _ := fresh.Output(id)
	if !reflect.DeepEqual(a, b) || a.Status != agent.OutputComplete || a.Through != fresh.Cursor() {
		t.Fatal(a, b)
	}
	ah, err := live.History(s.Root(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	bh, err := fresh.History(s.Root(), 0, 100)
	if err != nil || !reflect.DeepEqual(ah, bh) {
		t.Fatal(ah, bh, err)
	}
}
