package harness_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/work"
)

// The user talks to the manager directly: a message goes to its inbox and its
// final text reply reaches the user.
func TestManagerAnswersTheUserDirectly(t *testing.T) {
	s, err := harness.New(context.Background(), testConfig(t, false), harness.Dependencies{Provider: textResponse("Widget renamed.")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	manager := s.Manager()
	if _, err = s.Send(manager, "Please rename the widget"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		e, err := s.NextEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if v, ok := e.(conversation.MessageEvent); ok && v.Message.From == manager {
			if v.Message.To != message.User || v.Message.Kind != message.Reply || v.Message.Content != "Widget renamed." {
				t.Fatalf("manager sent %+v", v.Message)
			}
			return
		}
	}
}

// delegatingManager assigns one implementation, waits, and reports once it
// is woken by the submission.
type delegatingManager struct {
	calls    atomic.Int32
	reported chan struct{}
}

func (p *delegatingManager) Submit(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	switch n := p.calls.Add(1); n {
	case 1:
		return operation("create_agent", map[string]any{"role": "implementor"})
	case 2:
		var created struct {
			AgentID string `json:"agent_id"`
		}
		if err := json.Unmarshal([]byte(lastResult(r)), &created); err != nil {
			return provider.Response{}, err
		}
		return operation("assign_task", map[string]any{"kind": "implementation", "assignee": created.AgentID, "task": "Rename the widget", "context": nil, "expected_output": nil, "scope": nil})
	case 3:
		return operation("wait_for_input", struct{}{})
	default:
		select {
		case p.reported <- struct{}{}:
		default:
		}
		return provider.Response{Content: fmt.Sprintf("Report %d: submitted, audit next.", n)}, nil
	}
}

// submittingWorker submits its assignment and then hands off with text, the
// closing message that used to wake its owner a second time; it must no
// longer be asked for one.
type submittingWorker struct {
	calls    atomic.Int32
	reported chan struct{}
}

func (p *submittingWorker) Submit(ctx context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	if p.calls.Add(1) > 1 {
		select {
		case <-p.reported:
		case <-ctx.Done():
			return provider.Response{}, ctx.Err()
		}
		time.Sleep(50 * time.Millisecond) // Let the owner's exchange end.
		return provider.Response{Content: "Submitted for independent audit."}, nil
	}
	for _, m := range r.Messages {
		if m.Envelope != nil && m.Envelope.Work != nil {
			w := m.Envelope.Work
			return operation("submit_work", map[string]any{"work_id": w.ID, "expected_revision": w.Revision, "summary": "Renamed.", "evidence": nil, "artifacts": nil})
		}
	}
	return provider.Response{}, fmt.Errorf("no assignment")
}

// A worker's successful submission ends its turn: it writes no closing reply,
// so nothing wakes its owner a second time. The second wake used to produce a
// second, redundant report. Audits are manual here, so no auditor joins in.
func TestSubmissionEndsTheWorkerTurnWithoutAHandoff(t *testing.T) {
	reported := make(chan struct{}, 1)
	manager, worker := &delegatingManager{reported: reported}, &submittingWorker{reported: reported}
	cfg := testConfig(t, false)
	cfg.ManualAudits = true
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: textResponse("noted"), Manager: harness.AgentDependencies{Provider: manager}, Implementor: harness.AgentDependencies{Provider: worker}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	id := startManager(t, s, "Rename the widget")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var reports int
	handoff := false
	settle := time.After(10 * time.Second)
	for {
		wait, stop := context.WithTimeout(ctx, 500*time.Millisecond)
		e, err := s.NextEvent(wait)
		stop()
		if err != nil {
			if reports > 0 && ctx.Err() == nil {
				break // Quiet after the report: nothing else is coming.
			}
			select {
			case <-settle:
				t.Fatal("never settled", reports)
			default:
			}
			continue
		}
		if v, ok := e.(conversation.MessageEvent); ok && v.Message.Kind == message.Reply {
			switch {
			case v.Message.From == id:
				reports++
			case v.Message.To == id:
				handoff = true
			}
		}
	}
	if reports != 1 || handoff || worker.calls.Load() != 1 {
		t.Fatalf("manager reported %d times; handoff %v; worker asked %d times", reports, handoff, worker.calls.Load())
	}
	page, err := s.ListWork(ctx, id, work.ListQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].State != work.NeedsCheck {
		t.Fatal(page, err)
	}
}

// inspect_agent reads projected transcripts. A before position past the end
// reads the latest entries: an agent with no earlier page asked for before=100 on
// a 13-entry transcript and got an event-cursor error it could not act on.
func TestInspectAgentPastTheEndReadsTheLatest(t *testing.T) {
	s, err := harness.New(context.Background(), testConfig(t, false), harness.Dependencies{Provider: textResponse("ready")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	if _, err := s.Send(s.Manager(), "hello"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		e, err := s.NextEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if m, ok := e.(conversation.MessageEvent); ok && m.Message.To == message.User {
			break
		}
	}
	latest, err := s.InspectAgentContext(ctx, s.Manager(), conversation.InspectOptions{Transcript: &agent.TranscriptQuery{}})
	if err != nil || latest.Transcript == nil || len(latest.Transcript.Entries) == 0 {
		t.Fatal(latest, err)
	}
	last := latest.Transcript.Entries[len(latest.Transcript.Entries)-1].Position
	for _, before := range []uint64{last + 1, last + 2, 100} {
		past, err := s.InspectAgentContext(ctx, s.Manager(), conversation.InspectOptions{Transcript: &agent.TranscriptQuery{Before: before, Limit: 50}})
		if err != nil || past.Transcript == nil || len(past.Transcript.Entries) != len(latest.Transcript.Entries) || past.Transcript.Entries[len(past.Transcript.Entries)-1].Position != last {
			t.Fatalf("before=%d: %+v %v", before, past.Transcript, err)
		}
	}
	earlier, err := s.InspectAgentContext(ctx, s.Manager(), conversation.InspectOptions{Transcript: &agent.TranscriptQuery{Before: last}})
	if err != nil || len(earlier.Transcript.Entries) != len(latest.Transcript.Entries)-1 {
		t.Fatalf("paging before the last entry: %+v %v", earlier.Transcript, err)
	}
}

// messagesUser tries to reach the user and an unknown agent with
// send_message, then replies with what the tools said.
type messagesUser struct{ calls atomic.Int32 }

func (p *messagesUser) Submit(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	switch p.calls.Add(1) {
	case 1:
		return operation("send_message", map[string]any{"to": "user", "message": "What separator?"})
	case 2:
		return operation("send_message", map[string]any{"to": "agent-9", "message": "hello"})
	default:
		var refusals []string
		for _, m := range r.Messages {
			if m.Role == "tool" {
				refusals = append(refusals, m.Content.Text())
			}
		}
		return provider.Response{Content: strings.Join(refusals, "\n")}, nil
	}
}

// The user hears only the manager's replies, and reaches only the manager:
// send_message to the user or to anyone off the topology is refused, and the
// user cannot message a worker.
func TestManagerReachesTheUserOnlyByReply(t *testing.T) {
	s, err := harness.New(context.Background(), testConfig(t, false), harness.Dependencies{Manager: harness.AgentDependencies{Provider: &messagesUser{}}, Provider: textResponse("ok")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	worker := createWorker(t, s, roster.Implementor)
	if _, err := s.Send(worker, "bypass"); err == nil {
		t.Fatal("the user reached a worker directly")
	}
	if _, err := s.Send(s.Manager(), "go"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var toUser []message.Message
	for len(toUser) == 0 {
		e, err := s.NextEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if m, ok := e.(conversation.MessageEvent); ok && m.Message.To == message.User {
			toUser = append(toUser, m.Message)
		}
	}
	reply := toUser[0]
	if reply.From != s.Manager() || reply.Kind != message.Reply || !strings.Contains(reply.Content, "answer the user with a text reply") || !strings.Contains(reply.Content, "no message edge") {
		t.Fatalf("the user got %s %q", reply.Kind, reply.Content)
	}
}

// messagesItself sends a message to its own id, as an auditor did in a ladder
// run, then stops.
type messagesItself struct{ calls atomic.Int32 }

func (p *messagesItself) Submit(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	if p.calls.Add(1) == 1 {
		return operation("send_message", map[string]any{"to": string(r.Agent), "message": "note to self"})
	}
	return provider.Response{Content: "Done."}, nil
}

// No agent messages itself: the topology has no such edge, and only a host
// notice to its own owner is exempt from routing.
func TestAnAgentCannotMessageItself(t *testing.T) {
	s, err := harness.New(context.Background(), testConfig(t, false), harness.Dependencies{Manager: harness.AgentDependencies{Provider: &messagesItself{}}, Provider: textResponse("ok")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	startManager(t, s, "go")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		e, err := s.NextEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if m, ok := e.(conversation.MessageEvent); ok && m.Message.From == s.Manager() && m.Message.To == s.Manager() && m.Message.Kind != message.Notification {
			t.Fatalf("the manager messaged itself: %+v", m.Message)
		}
		if v, ok := e.(conversation.ToolEvent); ok && v.Agent == s.Manager() && v.Activity.Call.Name == "send_message" && !v.Activity.FinishedAt.IsZero() {
			if v.Activity.Err == nil || !strings.Contains(v.Activity.Err.Error(), "no message edge") {
				t.Fatalf("self-message: %v", v.Activity.Err)
			}
			return
		}
	}
}

// messagesItsReport sends its report to the user with send_message, as the
// manager once messaged its report upward in ladder easy-07, then ends its
// turn with the report as well.
type messagesItsReport struct{ calls atomic.Int32 }

func (p *messagesItsReport) Submit(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	if p.calls.Add(1) == 1 {
		return operation("send_message", map[string]any{"to": "user", "message": "Done and audited."})
	}
	return provider.Response{Content: "Done and audited."}, nil
}

// The manager holds only a reply edge to the user: it reports by replying,
// and a report sent with send_message is refused, so the user hears it once.
func TestManagerReportsOnlyByReplying(t *testing.T) {
	s, err := harness.New(context.Background(), testConfig(t, false), harness.Dependencies{Manager: harness.AgentDependencies{Provider: &messagesItsReport{}}, Provider: textResponse("ok")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	startManager(t, s, "go")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	refused := false
	for {
		e, err := s.NextEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if v, ok := e.(conversation.ToolEvent); ok && v.Agent == s.Manager() && v.Activity.Call.Name == "send_message" && !v.Activity.FinishedAt.IsZero() {
			if v.Activity.Err == nil || !strings.Contains(v.Activity.Err.Error(), "answer the user with a text reply") {
				t.Fatalf("send_message to the user: %v", v.Activity.Err)
			}
			refused = true
		}
		if m, ok := e.(conversation.MessageEvent); ok && m.Message.From == s.Manager() && m.Message.To == message.User {
			if !refused || m.Message.Kind != message.Reply || m.Message.Content != "Done and audited." {
				t.Fatalf("the user got %s %q (send_message refused first: %v)", m.Message.Kind, m.Message.Content, refused)
			}
			return
		}
	}
}
