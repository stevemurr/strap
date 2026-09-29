package harness_test

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/roster"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

// textResponse is a provider that answers every request with a fixed text.
type textResponse string

func (p textResponse) Submit(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
	return provider.Response{Content: string(p)}, nil
}

// startManager gives the manager a task the way a user does: in a message to
// its inbox.
func startManager(t *testing.T, s *harness.Session, task string) identity.ActorID {
	t.Helper()
	if _, err := s.Send(s.Manager(), task); err != nil {
		t.Fatal(err)
	}
	return s.Manager()
}

func testConfig(t *testing.T, localTools bool) harness.Config {
	cfg := harness.DefaultConfig()
	cfg.Dir, cfg.Web, cfg.LocalTools = t.TempDir(), nil, localTools
	return cfg
}

type closer struct {
	calls atomic.Int32
	fail  atomic.Bool
}

func (c *closer) Close(context.Context) error {
	c.calls.Add(1)
	if c.fail.Swap(false) {
		return errors.New("cleanup failed")
	}
	return nil
}

func TestHeadlessSessionOwnsAssemblyAndWork(t *testing.T) {
	cfg := testConfig(t, true)
	cfg.ManualAudits = true // The test assigns the audit itself.
	owned := &closer{}
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: textResponse("ready"), Resources: []harness.OwnedResource{{Name: "test", Resource: owned}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	// A session starts with its one manager, idle, which the user talks to.
	if a := s.Agents(); len(a) != 1 || a[0].ID != s.Manager() || a[0].Role != roster.Manager || a[0].Parent != message.User || a[0].State != agent.Idle {
		t.Fatal(a)
	}
	cfg.Manager.Prompt.Instructions[0] = "caller mutation"
	copy := s.Config()
	copy.Manager.Prompt.Instructions[0] = "inspection mutation"
	if s.Config().Manager.Prompt.Instructions[0] == "inspection mutation" || s.Config().Manager.Prompt.Instructions[0] == "caller mutation" {
		t.Fatal("configuration aliases caller")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	w, err := s.AssignWork(ctx, s.Manager(), work.AssignmentRequest{Kind: work.Implementation, Assignee: createWorker(t, s, roster.Implementor), Task: "test"})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := s.SubmitWork(ctx, w.Assignee, work.SubmitRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Summary: "done"})
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.GetWork(ctx, s.Manager(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := s.AssignWork(ctx, s.Manager(), work.AssignmentRequest{Kind: work.AuditWork, Assignee: createWorker(t, s, roster.Auditor), WorkID: w.ID, ExpectedRevision: w.Revision, SubmissionID: sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SubmitAudit(ctx, audit.Assignee, work.AuditRequest{WorkTarget: work.WorkTarget{ID: audit.ID, ExpectedRevision: audit.Revision}, SubmissionID: sub.ID, Verdict: work.Pass, Summary: "verified"})
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.GetWork(ctx, s.Manager(), w.ID)
	if err != nil || w.State != work.Accepted {
		t.Fatal(w, err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if owned.calls.Load() != 1 {
		t.Fatal("resource closed more than once")
	}
}

type observeProvider struct{ requests chan provider.Request }

func (p *observeProvider) Submit(_ context.Context, r provider.Request, observer provider.Observer) (provider.Response, error) {
	p.requests <- r
	return provider.Response{Content: "ok"}, nil
}
func TestDefaultRoleToolsPreserveCLIOrder(t *testing.T) {
	p := &observeProvider{requests: make(chan provider.Request, 8)}
	cfg := testConfig(t, true)
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	manager := startManager(t, s, "plan")
	var request provider.Request
	for request = <-p.requests; request.Agent != manager; request = <-p.requests {
	}
	var names []string
	for _, tool := range request.Tools {
		names = append(names, tool.Name)
	}
	// The manager plans, assigns and reports: no files, no shell, and nothing
	// to submit, since it never holds work. Reviewers read the workspace for it.
	want := []string{"get_audit", "get_plan", "get_work", "get_work_progress", "get_brief", "get_conclusion", "wait_for_input", "create_agent", "create_plan", "add_step", "edit_step", "cancel_steps", "reorder_steps", "rename_plan", "assign_task", "assign_repair", "cancel_work", "list_work", "send_message", "message_status", "stop_agent", "pause_agent", "resume_agent", "inspect_agent", "list_agents"}
	if !reflect.DeepEqual(names, want) {
		t.Fatal(names)
	}
	w, err := s.AssignWork(ctx, s.Manager(), work.AssignmentRequest{Kind: work.Implementation, Assignee: createWorker(t, s, roster.Implementor), Task: "do work"})
	if err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case request = <-p.requests:
			if request.Agent == w.Assignee {
				goto worker
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
worker:
	for _, d := range request.Tools {
		if d.Name == "assign_task" || d.Name == "assign_audit" || d.Name == "assign_repair" || d.Name == "submit_audit" {
			t.Fatalf("worker got %s", d.Name)
		}
	}
	info, err := s.InspectAgent(w.Assignee, conversation.InspectOptions{Transcript: &agent.TranscriptQuery{}})
	if err != nil || info.Transcript == nil {
		t.Fatal(info, err)
	}
}

func TestStartupFailureReturnsRetryableCleanupOwnership(t *testing.T) {
	owned := &closer{}
	owned.fail.Store(true)
	cfg := harness.DefaultConfig()
	cfg.Model.BaseURL = "invalid"
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Resources: []harness.OwnedResource{{Name: "test", Resource: owned}}})
	if s != nil || err == nil {
		t.Fatal("partial session returned")
	}
	var cleanup *harness.StartupError
	if !errors.As(err, &cleanup) {
		t.Fatal(err)
	}
	if err := cleanup.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if owned.calls.Load() != 2 {
		t.Fatal(owned.calls.Load())
	}
}

func createWorker(t *testing.T, s *harness.Session, role roster.Role) identity.ActorID {
	t.Helper()
	r, e := s.CreateAgent(context.Background(), s.Manager(), roster.CreateRequest{Role: role})
	if e != nil {
		t.Fatal(e)
	}
	return r.AgentID
}

// With the web enabled, only researchers get it; everything else the session
// learns from outside arrives as their research. The deep researcher reaches
// the web only through the deep research engine.
func TestOnlyResearchersHoldWebTools(t *testing.T) {
	cfg := testConfig(t, true)
	cfg.Web = &tool.WebConfig{}
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: textResponse("ready")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	c := s.Configuration()
	if c.DeepResearcher == nil {
		t.Fatal("deep researcher missing with the web enabled")
	}
	for role, tools := range map[roster.Role][]provider.ToolDefinition{roster.Manager: c.Manager.Tools, roster.Implementor: c.Implementor.Tools, roster.Auditor: c.Auditor.Tools, roster.WebResearcher: c.WebResearcher.Tools, roster.DeepResearcher: c.DeepResearcher.Tools} {
		web, deep := 0, 0
		for _, d := range tools {
			switch d.Name {
			case "web_search", "open_url":
				web++
			case "deep_research":
				deep++
			}
		}
		if want := map[bool]int{true: 2, false: 0}[role == roster.WebResearcher]; web != want {
			t.Errorf("%s holds %d web tools, want %d", role, web, want)
		}
		if want := map[bool]int{true: 1, false: 0}[role == roster.DeepResearcher]; deep != want {
			t.Errorf("%s holds %d deep_research tools, want %d", role, deep, want)
		}
	}
}

// Every role follows the session model's thinking setting; none is turned
// off behind the caller's back.
func TestEveryRoleKeepsTheSessionThinking(t *testing.T) {
	for _, on := range []bool{true, false} {
		cfg := testConfig(t, false)
		cfg.Model.Generation.EnableThinking = &on
		s, err := harness.New(context.Background(), cfg, harness.Dependencies{})
		if err != nil {
			t.Fatal(err)
		}
		c := s.Configuration()
		s.Close(context.Background())
		for role, m := range map[string]*harness.ModelConfig{"manager": c.Manager.Model, "implementor": c.Implementor.Model, "auditor": c.Auditor.Model, "web_researcher": c.WebResearcher.Model} {
			if g := m.Generation.EnableThinking; g == nil || *g != on {
				t.Fatalf("thinking=%v: %s thinking %v", on, role, g)
			}
		}
	}
}

// The session id is a function of the seed, like every other id.
func TestSessionIDDerivesFromSeed(t *testing.T) {
	open := func(seed string) string {
		cfg := testConfig(t, false)
		cfg.Seed = []byte(seed)
		s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: textResponse("ready")})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close(context.Background())
		return s.ID()
	}
	if a, b, c := open("one"), open("one"), open("two"); a != b || a == c || len(a) != 32 {
		t.Fatal(a, b, c)
	}
}
