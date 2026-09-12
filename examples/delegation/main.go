// A scripted implementation -> failed audit -> repair -> passing audit.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/internal/workflow"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/prompt"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

func ptr[T any](v T) *T { return &v }

type cycleProvider struct {
	mu       sync.Mutex
	session  *workflow.Session
	plan     work.Plan
	root     message.ActorID
	assigned bool
	reviews  map[work.SubmissionID]bool
	calls    int
	failed   bool
	done     chan work.Work
}

func (p *cycleProvider) Submit(ctx context.Context, r provider.Request) (provider.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	invoke := func(name string, args any) (provider.Response, error) {
		raw, e := json.Marshal(args)
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: fmt.Sprintf("call-%d", p.calls), Name: name, Arguments: raw}}}, e
	}
	for _, m := range r.Messages {
		if m.Role == "tool" && strings.HasPrefix(m.Content.Text(), "Tool error:") {
			return provider.Response{}, fmt.Errorf("script tool failed: %s", m.Content.Text())
		}
	}
	if r.Agent == p.root {
		if !p.assigned {
			p.assigned = true
			return invoke("assign_work", tool.AssignWorkArgs{Kind: work.Implementation, Task: "implement storage", Scope: &work.Scope{PlanID: p.plan.ID, StepIDs: []work.StepID{p.plan.Steps[0].ID, p.plan.Steps[1].ID}}})
		}
		for i := len(r.Messages) - 1; i >= 0; i-- {
			m := r.Messages[i]
			if m.Envelope == nil || m.Envelope.Event == nil {
				continue
			}
			e := m.Envelope.Event
			if e.Kind == work.AuditCompleted && e.Work.State == work.Accepted {
				select {
				case p.done <- e.Work:
				default:
				}
				return provider.Response{Content: "Audited outcome accepted."}, nil
			}
			if e.Kind == work.ReviewRequested && !p.reviews[e.SubmissionID] {
				w, err := p.session.Store.GetWork(p.root, e.Work.ID)
				if err != nil {
					return provider.Response{}, err
				}
				if w.State != work.NeedsCheck {
					continue
				}
				p.reviews[e.SubmissionID] = true
				return invoke("assign_work", tool.AssignWorkArgs{Kind: work.AuditWork, WorkID: w.ID, ExpectedRevision: w.Revision, SubmissionID: w.LatestSubmissionID})
			}
		}
		return provider.Response{Content: "Waiting for the work cycle."}, nil
	}
	var snapshot *work.Work
	for _, m := range r.Messages {
		if m.Envelope != nil && m.Envelope.Work != nil {
			snapshot = m.Envelope.Work
		}
	}
	if snapshot == nil {
		return provider.Response{}, fmt.Errorf("missing work envelope")
	}
	w, err := p.session.Store.GetWork(r.Agent, snapshot.ID)
	if err != nil {
		return provider.Response{}, err
	}
	for _, def := range r.Tools {
		if def.Name == "assign_work" || def.Name == "create_agent" {
			return provider.Response{}, fmt.Errorf("delegation leaked to worker")
		}
		if w.Kind == work.AuditWork && def.Name == "submit_work" {
			return provider.Response{}, fmt.Errorf("implementation tool leaked to auditor")
		}
	}
	if w.State != work.Active {
		return provider.Response{Content: "Outcome submitted."}, nil
	}
	target := work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}
	if w.Kind == work.AuditWork {
		req := work.AuditRequest{WorkTarget: target, SubmissionID: w.SubjectSubmissionID, Verdict: work.Pass, Summary: "All requirements verified"}
		if !p.failed {
			p.failed = true
			req.Verdict = work.Fail
			req.Summary = "Missing error handling"
			req.Findings = []work.Finding{{StepIDs: []work.StepID{p.plan.Steps[0].ID}, Description: "write failure ignored", RequiredChange: "propagate failure", Verification: "test failed write"}}
		}
		return invoke("submit_audit", req)
	}
	plan, err := p.session.Store.GetPlan(r.Agent, w.Scope.PlanID)
	if err != nil {
		return provider.Response{}, err
	}
	var changes []work.StepProgress
	for _, step := range plan.Steps {
		for _, id := range w.Scope.StepIDs {
			if step.ID == id && step.Status != work.ReadyForReview {
				changes = append(changes, work.StepProgress{ID: id, Status: ptr(work.ReadyForReview)})
			}
		}
	}
	if len(changes) > 0 {
		return invoke("update_plan", work.ProgressUpdate{WorkTarget: target, Steps: changes})
	}
	return invoke("submit_work", work.SubmitRequest{WorkTarget: target, Summary: "Implemented and checked", Evidence: []string{"scripted evidence"}})
}
func run(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	p := &cycleProvider{reviews: map[work.SubmissionID]bool{}, done: make(chan work.Work, 1)}
	c := conversation.New(ctx)
	s := workflow.New(ctx, c, agent.Spec{Provider: p, Prompt: prompt.Prompt{Role: "implementor"}}, agent.Spec{Provider: p, Prompt: prompt.Prompt{Role: "auditor"}})
	p.session = s
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = s.Close(cleanup)
	}()
	_, err := c.CreateAgent(message.User, agent.Spec{Provider: p, Prompt: prompt.Prompt{Role: "root"}, Tools: s.RootTools()})
	if err != nil {
		return err
	}
	p.root = c.Root()
	p.plan, err = s.Store.UpdatePlan(p.root, work.PlanUpdate{Title: ptr("Storage"), Steps: []work.StepEdit{{Title: ptr("Implement")}, {Title: ptr("Test")}, {Title: ptr("Integrate")}}})
	if err != nil {
		return err
	}
	if _, err = c.Send(c.Root(), "Begin"); err != nil {
		return err
	}
	return reportOutcome(ctx, s, p)
}

func reportOutcome(ctx context.Context, s *workflow.Session, p *cycleProvider) error {
	select {
	case w := <-p.done:
		fmt.Printf("%s accepted after failed audit, scoped repair, and passing audit.\n", w.ID)
		plan, e := s.Store.GetPlan(p.root, p.plan.ID)
		if e != nil {
			return e
		}
		for _, step := range plan.Steps {
			fmt.Printf("%s: %s\n", step.Title, step.Status)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func main() { mainWithExit(context.Background(), os.Exit) }

func mainWithExit(ctx context.Context, exit func(int)) {
	if err := run(ctx); err != nil {
		log.Print(err)
		exit(1)
	}
}
