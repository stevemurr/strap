package harness_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
)

// soloScript writes a file and tries to reply; when the finish check holds
// the reply, it runs command and replies again.
type soloScript struct {
	calls   atomic.Int32
	write   bool
	command string
	tools   atomic.Value // []string offered on the first call
	notice  atomic.Value // The text that held the reply.
}

func (p *soloScript) Submit(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	n := p.calls.Add(1)
	call := func(name, args string) provider.Response {
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: fmt.Sprintf("c%d", n), Name: name, Arguments: json.RawMessage(args)}}}
	}
	if !p.write {
		n++ // Nothing to write: reply at once.
	}
	switch n {
	case 1:
		var names []string
		for _, d := range r.Tools {
			names = append(names, d.Name)
		}
		p.tools.Store(names)
		return call("write_file", `{"input":{"path":"answer.txt","content":"42\n"}}`), nil
	case 2:
		return provider.Response{Content: "Done."}, nil
	case 3:
		p.notice.Store(r.Messages[len(r.Messages)-1].Content.Text())
		return call("shell", fmt.Sprintf(`{"input":{"command":%q,"timeout_ms":null}}`, p.command)), nil
	}
	return provider.Response{Content: "Done and checked."}, nil
}

func soloSession(t *testing.T, p *soloScript) *harness.Session {
	t.Helper()
	cfg := testConfig(t, true)
	cfg.Solo, cfg.LSP = true, nil
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: textResponse("unused"), Agent: harness.AgentDependencies{Provider: p}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Dispose(context.Background()) })
	return s
}

// soloReply returns the agent's reply to the user.
func soloReply(t *testing.T, s *harness.Session) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		e, err := s.NextEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if v, ok := e.(conversation.MessageEvent); ok && v.Message.From == s.Manager() && v.Message.To == message.User && v.Message.Kind == message.Reply {
			return v.Message.Content
		}
	}
}

// A solo session is one agent the user talks to, holding every tool itself
// and none for coordinating others.
func TestSoloSessionIsOneAgentWithEveryTool(t *testing.T) {
	p := &soloScript{write: true, command: "true"}
	s := soloSession(t, p)
	if a := s.Agents(); len(a) != 1 || a[0].ID != s.Manager() || a[0].Role != roster.Agent || a[0].Parent != message.User {
		t.Fatal(a)
	}
	if _, err := s.Send(s.Manager(), "write the answer"); err != nil {
		t.Fatal(err)
	}
	if got := soloReply(t, s); got != "Done and checked." {
		t.Fatal(got)
	}
	tools, _ := p.tools.Load().([]string)
	for _, want := range []string{"read_file", "write_file", "edit_file", "shell", "update_todos"} {
		if !slices.Contains(tools, want) {
			t.Fatalf("missing %s in %v", want, tools)
		}
	}
	for _, unwanted := range []string{"assign_task", "create_plan", "create_agent", "send_message", "wait_for_input", "submit_work"} {
		if slices.Contains(tools, unwanted) {
			t.Fatalf("solo agent was offered %s", unwanted)
		}
	}
	if s.Configuration().Agent == nil {
		t.Fatal("effective configuration omits the agent")
	}
}

// The finish check holds a reply once when the agent changed files and ran
// nothing since, and lets it through after a command that exits 0.
func TestSoloFinishCheckHoldsAnUncheckedChange(t *testing.T) {
	p := &soloScript{write: true, command: "true"}
	s := soloSession(t, p)
	if _, err := s.Send(s.Manager(), "write the answer"); err != nil {
		t.Fatal(err)
	}
	if got := soloReply(t, s); got != "Done and checked." {
		t.Fatal(got)
	}
	notice, _ := p.notice.Load().(string)
	if !strings.Contains(notice, "You changed answer.txt and have run nothing since") {
		t.Fatal(notice)
	}
	if p.calls.Load() != 4 {
		t.Fatal("calls", p.calls.Load())
	}
}

// failScript writes a file, runs a command that fails, and replies; held,
// it replies again.
type failScript struct {
	calls  atomic.Int32
	notice atomic.Value
}

func (p *failScript) Submit(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	n := p.calls.Add(1)
	call := func(name, args string) provider.Response {
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: fmt.Sprintf("c%d", n), Name: name, Arguments: json.RawMessage(args)}}}
	}
	switch n {
	case 1:
		return call("write_file", `{"input":{"path":"answer.txt","content":"42\n"}}`), nil
	case 2:
		return call("shell", `{"input":{"command":"exit 3","timeout_ms":null}}`), nil
	case 3:
		return provider.Response{Content: "Done."}, nil
	}
	p.notice.Store(r.Messages[len(r.Messages)-1].Content.Text())
	return provider.Response{Content: "Done anyway."}, nil
}

// A command that fails after the change is named in the hold; the check holds
// once, so the reply that follows goes out.
func TestSoloFinishCheckNamesAFailingCommand(t *testing.T) {
	p := &failScript{}
	cfg := testConfig(t, true)
	cfg.Solo, cfg.LSP = true, nil
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: textResponse("unused"), Agent: harness.AgentDependencies{Provider: p}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Dispose(context.Background()) })
	if _, err := s.Send(s.Manager(), "write the answer"); err != nil {
		t.Fatal(err)
	}
	if got := soloReply(t, s); got != "Done anyway." {
		t.Fatal(got)
	}
	if notice, _ := p.notice.Load().(string); !strings.Contains(notice, "`exit 3` exited with status 3") {
		t.Fatal(notice)
	}
}

// A question answered without changing anything is not held.
func TestSoloFinishCheckLeavesAnswersAlone(t *testing.T) {
	p := &soloScript{}
	s := soloSession(t, p)
	if _, err := s.Send(s.Manager(), "what is in answer.txt?"); err != nil {
		t.Fatal(err)
	}
	if got := soloReply(t, s); got != "Done." {
		t.Fatal(got)
	}
	if p.calls.Load() != 1 {
		t.Fatal("calls", p.calls.Load())
	}
}

// todoScript sets a todo list and replies.
type todoScript struct{ calls atomic.Int32 }

func (p *todoScript) Submit(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
	if p.calls.Add(1) == 1 {
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: "t", Name: "update_todos", Arguments: json.RawMessage(`{"input":{"todos":[{"content":"Read the README","status":"completed"},{"content":"Implement Order","status":"in_progress"},{"content":"Run the tests","status":"pending"}]}}`)}}}, nil
	}
	return provider.Response{Content: "Planned."}, nil
}

// A solo agent's todo list reaches the event stream whole, for the user to
// follow its plan.
func TestSoloTodoListIsPublished(t *testing.T) {
	cfg := testConfig(t, true)
	cfg.Solo, cfg.LSP = true, nil
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: textResponse("unused"), Agent: harness.AgentDependencies{Provider: &todoScript{}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Dispose(context.Background()) })
	if _, err := s.Send(s.Manager(), "build it"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var todos *conversation.TodosEvent
	var result string
	for {
		e, err := s.NextEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		switch v := e.(type) {
		case conversation.TodosEvent:
			todos = &v
		case conversation.ToolEvent:
			if v.Activity.Call.Name == "update_todos" && !v.Activity.FinishedAt.IsZero() {
				result = v.Activity.Result.Content.Text()
			}
		}
		if m, ok := e.(conversation.MessageEvent); ok && m.Message.To == message.User {
			break
		}
	}
	if todos == nil || todos.Agent != s.Manager() || len(todos.Todos) != 3 || todos.Todos[1] != (conversation.Todo{Content: "Implement Order", Status: "in_progress"}) {
		t.Fatal(todos)
	}
	if result != "Todo list updated: 1 completed, 1 in progress, 1 pending." {
		t.Fatal(result)
	}
}

// testedAgent changes a file, checks it, and replies; held, it replies again.
type testedAgent struct {
	calls  atomic.Int32
	notice atomic.Value
}

func (p *testedAgent) Submit(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	n := p.calls.Add(1)
	call := func(name, args string) provider.Response {
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: fmt.Sprintf("a%d", n), Name: name, Arguments: json.RawMessage(args)}}}
	}
	switch n {
	case 1:
		return call("write_file", `{"input":{"path":"answer.txt","content":"42\n"}}`), nil
	case 2:
		return call("shell", `{"input":{"command":"true","timeout_ms":null}}`), nil
	case 3:
		return provider.Response{Content: "Done."}, nil
	}
	p.notice.Store(r.Messages[len(r.Messages)-1].Content.Text())
	return provider.Response{Content: "Fixed."}, nil
}

// adversary writes a failing test and reports it, then reports a command
// that passes, which the harness must not pass on.
type adversary struct{ calls atomic.Int32 }

func (p *adversary) Submit(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	n := p.calls.Add(1)
	call := func(name, args string) provider.Response {
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: fmt.Sprintf("t%d", n), Name: name, Arguments: json.RawMessage(args)}}}
	}
	switch n {
	case 1:
		return call("write_file", `{"input":{"path":"answer_check.sh","content":"echo want 43; exit 1\n"}}`), nil
	case 2:
		return call("report_failure", `{"input":{"path":"answer_check.sh","command":"sh answer_check.sh","requirement":"the answer is 43"}}`), nil
	case 3:
		return call("report_failure", `{"input":{"path":"answer_check.sh","command":"true","requirement":"anything"}}`), nil
	}
	// Long enough that its run is framed into content chunks, as a live
	// tester's transcript is.
	return provider.Response{Content: "Covered the answer. " + strings.Repeat("x", 100<<10)}, nil
}

// With the tester on, a checked change is tested before the reply goes out:
// only the failure the harness reproduced reaches the agent, and the tester's
// files stay in its own copy of the workspace.
func TestSoloTesterSendsBackReproducedFailures(t *testing.T) {
	a, adv := &testedAgent{}, &adversary{}
	cfg := testConfig(t, true)
	cfg.Solo, cfg.Tester, cfg.LSP = true, true, nil
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: textResponse("unused"), Agent: harness.AgentDependencies{Provider: a}, Tester: harness.AgentDependencies{Provider: adv}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Dispose(context.Background()) })
	if _, err := s.Send(s.Manager(), "write the answer to answer.txt"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var run *conversation.TesterEvent
	var reply string
	for reply == "" {
		e, err := s.NextEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		switch v := e.(type) {
		case conversation.TesterEvent:
			run = &v
		case conversation.MessageEvent:
			if v.Message.To == message.User && v.Message.Kind == message.Reply {
				reply = v.Message.Content
			}
		}
	}
	if reply != "Fixed." {
		t.Fatal(reply)
	}
	if run == nil || len(run.Failures) != 1 || run.Failures[0].Command != "sh answer_check.sh" || !strings.Contains(run.Failures[0].Output, "want 43") || run.Request != "write the answer to answer.txt" || len(run.Transcript) == 0 || !strings.Contains(run.Transcript[len(run.Transcript)-1].Content.Text(), "Covered the answer.") || len(run.Steps) != run.Calls || run.Steps[1].Calls[0] != "report_failure" {
		t.Fatalf("%+v", run)
	}
	notice, _ := a.notice.Load().(string)
	if !strings.Contains(notice, "the harness ran them against your change: 1 fail") || !strings.Contains(notice, "the answer is 43") || strings.Contains(notice, "anything") {
		t.Fatal(notice)
	}
	if _, err := os.Stat(filepath.Join(cfg.Dir, "answer_check.sh")); !os.IsNotExist(err) {
		t.Fatal("the tester wrote into the real workspace", err)
	}
}

// firstRequest records the agent's first request, then behaves as a.
type firstRequest struct {
	testedAgent
	first atomic.Value
}

func (p *firstRequest) Submit(ctx context.Context, r provider.Request, o provider.Observer) (provider.Response, error) {
	if p.calls.Load() == 0 {
		var all []string
		for _, m := range r.Messages {
			all = append(all, m.Content.Text())
			if m.Envelope != nil {
				all = append(all, m.Envelope.Content)
			}
		}
		p.first.Store(strings.Join(all, "\n"))
	}
	return p.testedAgent.Submit(ctx, r, o)
}

// Without -tester, a request runs the tester only when it asks for
// adversarial testing, and the agent is told the harness will do it.
func TestSoloTesterRunsWhenTheRequestAsks(t *testing.T) {
	for _, tc := range []struct {
		request string
		asked   bool
	}{
		{"write the answer to answer.txt, and run an adversarial tester on it", true},
		{"write the answer to answer.txt", false},
	} {
		a, adv := &firstRequest{}, &adversary{}
		cfg := testConfig(t, true)
		cfg.Solo, cfg.LSP = true, nil
		s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: textResponse("unused"), Agent: harness.AgentDependencies{Provider: a}, Tester: harness.AgentDependencies{Provider: adv}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Send(s.Manager(), tc.request); err != nil {
			t.Fatal(err)
		}
		want := "Done."
		if tc.asked {
			want = "Fixed."
		}
		if got := soloReply(t, s); got != want {
			t.Fatalf("%q: reply %q, want %q", tc.request, got, want)
		}
		first, _ := a.first.Load().(string)
		if told := strings.Contains(first, "the harness runs an independent tester"); told != tc.asked {
			t.Fatalf("%q: told %v\n%s", tc.request, told, first)
		}
		if ran := adv.calls.Load() > 0; ran != tc.asked {
			t.Fatalf("%q: tester ran %v", tc.request, ran)
		}
		s.Dispose(context.Background())
	}
}
