package machine

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/work"
)

// history builds timelines by hand for the checker's own tests.
type history struct{ tl Timeline }

const (
	mgr  = identity.ActorID("agent-2")
	impl = identity.ActorID("agent-3")
	aud  = identity.ActorID("agent-4")
)

func newHistory() *history {
	return &history{tl: Timeline{Manager: mgr,
		Roles:  map[identity.ActorID]roster.Role{mgr: roster.Manager, impl: roster.Implementor, aud: roster.Auditor},
		Parent: map[identity.ActorID]identity.ActorID{mgr: message.User, impl: mgr, aud: mgr},
		Works:  map[work.ID]work.Work{}, Plans: map[work.PlanID]work.Plan{},
		Registrations: []roster.Registration{
			{AgentID: mgr, Parent: roster.User, Role: roster.Manager},
			{AgentID: impl, Parent: mgr, Role: roster.Implementor},
			{AgentID: aud, Parent: mgr, Role: roster.Auditor},
		}}}
}

func sec(n int) time.Duration { return time.Duration(n) * time.Second }

func (h *history) user(at int, kind Intent, text string) *history {
	h.tl.Turns = append(h.tl.Turns, Turn{At: sec(at), Text: text, Intent: kind})
	return h.msg(at, message.User, mgr, message.Instruction, text)
}
func (h *history) msg(at int, from, to identity.ActorID, kind message.MessageKind, text string) *history {
	h.tl.Messages = append(h.tl.Messages, Message{sec(at), message.Message{From: from, To: to, Kind: kind, Content: text}})
	return h
}
func (h *history) call(at int, agent identity.ActorID, name, args string) *history {
	h.tl.Calls = append(h.tl.Calls, Call{At: sec(at), Start: sec(at), Agent: agent, Name: name, Args: args})
	return h
}
func (h *history) work(at int, id work.ID, kind work.Kind, assignee identity.ActorID, from, to work.State) *history {
	w := h.tl.Works[id]
	w.ID, w.Kind, w.Owner, w.Assignee, w.State = id, kind, mgr, assignee, to
	if kind == work.Implementation {
		w.Scope = &work.Scope{PlanID: "plan-1", StepIDs: []work.StepID{"step-1"}}
	}
	if kind == work.AuditWork || kind == work.Repair {
		w.ParentID = "work-1"
	}
	h.tl.Works[id] = w
	h.tl.WorkLog = append(h.tl.WorkLog, WorkChange{At: sec(at), ID: id, Parent: w.ParentID, Kind: kind, Owner: mgr, Assignee: assignee, From: from, To: to})
	return h
}
func (h *history) write(at int, agent identity.ActorID, path string) *history {
	h.tl.Writes = append(h.tl.Writes, Write{At: sec(at), Agent: agent, Path: path, Tool: "write_file"})
	return h
}
func (h *history) step(at int, from, to work.StepStatus) *history {
	h.tl.StepLog = append(h.tl.StepLog, StepChange{At: sec(at), Plan: "plan-1", Step: "step-1", Owner: mgr, From: from, To: to})
	h.tl.Plans["plan-1"] = work.Plan{ID: "plan-1", Owner: mgr, Steps: []work.Step{{ID: "step-1", Title: "Do it", Status: to}}}
	return h
}

// happy is the protocol as designed: a chat and then a task, each sent to the
// session's manager; the chat answered directly, the task planned,
// implemented, audited, accepted and reported.
func happy() *history {
	return newHistory().
		user(0, Chat, "Hi! What is a slug?").
		msg(2, mgr, message.User, message.Reply, "A short URL-friendly name.").
		user(4, Task, "Implement it").
		call(10, mgr, "create_plan", `{}`).step(10, "", work.Pending).
		call(11, mgr, "create_agent", `{"input":{"role":"implementor"}}`).
		call(12, mgr, "assign_task", `{}`).work(12, "work-1", work.Implementation, impl, "", work.Active).
		call(13, mgr, "wait_for_input", `{}`).write(15, impl, "text.go").
		step(20, work.Pending, work.ReadyForReview).work(21, "work-1", work.Implementation, impl, work.Active, work.NeedsCheck).
		call(22, mgr, "create_agent", `{"input":{"role":"auditor"}}`).
		call(23, mgr, "assign_audit", `{}`).work(23, "work-1", work.Implementation, impl, work.NeedsCheck, work.Checking).work(23, "work-2", work.AuditWork, aud, "", work.Active).
		work(30, "work-1", work.Implementation, impl, work.Checking, work.Accepted).work(30, "work-2", work.AuditWork, aud, work.Active, work.Closed).step(30, work.ReadyForReview, work.Completed).
		msg(31, impl, mgr, message.Reply, "Submitted.").
		msg(32, mgr, message.User, message.Reply, "Done and audited.")
}

func TestMachineAcceptsTheDesignedProtocol(t *testing.T) {
	if v := happy().tl.Violations(true); len(v) > 0 {
		t.Fatalf("designed protocol rejected:\n%s", strings.Join(v, "\n"))
	}
	paths := happy().tl.Paths()
	for _, want := range []string{"user → reply → user → planning → staffing → assigning → waiting → staffing → assigning → reply", "new → active → needs_check → checking → accepted"} {
		if !strings.Contains(paths, want) {
			t.Errorf("paths lack %q:\n%s", want, paths)
		}
	}
}

// Each broken history names the rule it breaks.
func TestMachineRejectsProtocolViolations(t *testing.T) {
	cases := []struct {
		name, want string
		mutate     func(*history)
	}{
		{"audit skipped", `moved "active" → accepted`, func(h *history) {
			h.work(25, "work-3", work.Implementation, impl, "", work.Active).work(26, "work-3", work.Implementation, impl, work.Active, work.Accepted)
		}},
		{"step completed without accepted work", "without accepted or delivered work covering it", func(h *history) {
			h.tl.StepLog = append(h.tl.StepLog[:2], StepChange{At: sec(20), Plan: "plan-1", Step: "step-1", From: work.ReadyForReview, To: work.Completed})
		}},
		{"user message never answered", "conversation: no reply to the task at 40s", func(h *history) {
			h.user(40, Task, "Also this")
		}},
		{"question answered late", "took 2m40s to answer the question at 40s", func(h *history) {
			h.user(40, Question, "How is it going?").msg(200, mgr, message.User, message.Reply, "Fine.")
		}},
		{"ambiguous change not questioned", "the ambiguous change at 40s was not answered with a question", func(h *history) {
			h.user(40, Ambiguous, "Change it").msg(41, mgr, message.User, message.Reply, "Done.")
		}},
		{"manager messages its report", "agent-2 (manager) sent the user a instruction at 32s with no message edge", func(h *history) {
			h.msg(32, mgr, message.User, message.Instruction, "Done and audited.")
		}},
		{"manager messages the user mid-turn", "the user hears only replies", func(h *history) {
			h.msg(14, mgr, message.User, message.Instruction, "Which separator?")
		}},
		{"message without an edge", "with no reply edge", func(h *history) {
			h.msg(31, impl, message.User, message.Reply, "Done, going over my manager's head.")
		}},
		{"a second manager", "want exactly one manager", func(h *history) {
			h.tl.Roles["agent-9"], h.tl.Parent["agent-9"] = roster.Manager, message.User
		}},
		{"manager stopped mid-session", "lives as long as the session", func(h *history) {
			h.tl.Exits = append(h.tl.Exits, Exit{At: sec(15), ID: mgr, State: "stopped"})
		}},
		{"instruction never answered", "agent-2 never answered the user's message at 40s", func(h *history) {
			h.user(40, Update, "Also this")
		}},
		{"a non-researcher searches the web", "agent-3 (implementor) called web_search at 14s; only web researchers and a solo agent use the web", func(h *history) {
			h.call(14, impl, "web_search", `{}`)
		}},
		{"a web researcher runs deep research", "agent-5 (web_researcher) called deep_research at 14s; only a deep researcher runs it", func(h *history) {
			h.tl.Roles["agent-5"], h.tl.Parent["agent-5"] = roster.WebResearcher, mgr
			h.call(14, "agent-5", "deep_research", `{}`)
		}},
		{"a deep researcher searches the web itself", "agent-5 (deep_researcher) called web_search at 14s; only web researchers and a solo agent use the web", func(h *history) {
			h.tl.Roles["agent-5"], h.tl.Parent["agent-5"] = roster.DeepResearcher, mgr
			h.call(14, "agent-5", "web_search", `{}`)
		}},
		{"a deep researcher nobody asked for", "agent-2 created a deep researcher at 14s, but no user message before it asked for deep research", func(h *history) {
			h.call(14, mgr, "create_agent", `{"input":{"role":"deep_researcher"}}`)
		}},
		{"a deep researcher nobody asked for, staffed by its assignment", "agent-2 created a deep researcher at 14s, but no user message before it asked for deep research", func(h *history) {
			h.call(14, mgr, "assign_task", `{"input":{"kind":"deep_research","assignee":null,"task":"t"}}`)
		}},
		{"an experimenter's write escapes its copy", "agent-6 (experimenter) wrote text.go with shell at 15s; only implementors change the workspace", func(h *history) {
			h.tl.Roles["agent-6"], h.tl.Parent["agent-6"] = roster.Experimenter, mgr
			h.tl.Writes = append(h.tl.Writes, Write{At: sec(15), Agent: "agent-6", Path: "text.go", Tool: "shell"})
		}},
		{"implementation completes a step it never submitted", "step step-1 completed at 30s without accepted or delivered work covering it", func(h *history) {
			h.tl.StepLog[1] = StepChange{At: sec(20), Plan: "plan-1", Step: "step-1", From: work.Pending, To: work.Pending}
			h.tl.StepLog[2] = StepChange{At: sec(30), Plan: "plan-1", Step: "step-1", From: work.Pending, To: work.Completed}
		}},
		{"manager answers research from recall", "reported on the research at 40s at 42s without assigning any review, research or experiment", func(h *history) {
			h.user(40, Research, "What is the latest Go release?").msg(42, mgr, message.User, message.Reply, "Go 1.27.")
		}},
		{"manager edits", "the manager plans and assigns, implementors change the workspace", func(h *history) {
			h.call(15, mgr, "write_file", `{}`)
		}},
		{"a non-implementor writes", "agent-2 (manager) wrote text.go with shell at 15s; only implementors change the workspace", func(h *history) {
			h.tl.Writes = append(h.tl.Writes, Write{At: sec(15), Agent: mgr, Path: "text.go", Tool: "shell"})
		}},
		{"manager reports with live work", "live work items and no question", func(h *history) {
			h.msg(22, mgr, message.User, message.Reply, "All done.")
		}},
		{"duplicate report", "for the 3 time with 2 instructions received", func(h *history) {
			h.msg(35, mgr, message.User, message.Reply, "Already done.")
		}},
		{"self-audit", "not an independent auditor", func(h *history) {
			h.work(24, "work-4", work.AuditWork, impl, "", work.Active).work(25, "work-4", work.AuditWork, impl, work.Active, work.Closed)
		}},
		{"work left live", "is still active", func(h *history) {
			h.work(40, "work-5", work.Review, impl, "", work.Active)
		}},
		{"write after submitting", "agent-3 (implementor) wrote text.go with write_file at 25s without an active implementation", func(h *history) {
			h.write(25, impl, "text.go")
		}},
		{"implementation returned to needs_check without a cancelled audit", `moved "checking" → needs_check`, func(h *history) {
			h.work(25, "work-3", work.Implementation, impl, "", work.Active).work(26, "work-3", work.Implementation, impl, work.Active, work.NeedsCheck).
				work(27, "work-3", work.Implementation, impl, work.NeedsCheck, work.Checking).work(28, "work-3", work.Implementation, impl, work.Checking, work.NeedsCheck)
		}},
		{"two live work items write one file", "text.go written under work-1 (agent-3) and work-3 (agent-9) while both were live", func(h *history) {
			h.tl.Roles["agent-9"], h.tl.Parent["agent-9"] = roster.Implementor, mgr
			h.work(14, "work-3", work.Implementation, "agent-9", "", work.Active).write(16, "agent-9", "text.go")
		}},
		{"manager implements in its reply", "reported on the task at 40s at 42s without assigning any implementation", func(h *history) {
			h.user(40, Task, "Now the other one").msg(42, mgr, message.User, message.Reply, "Here is the code: func F() {}")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := happy()
			c.mutate(h)
			v := h.tl.Violations(true)
			if !strings.Contains(strings.Join(v, "\n"), c.want) {
				t.Fatalf("want a violation containing %q, got:\n%s", c.want, strings.Join(v, "\n"))
			}
		})
	}
}

// A deep researcher created after the user asked for deep research, running
// it, breaks no rule.
func TestMachineAcceptsRequestedDeepResearch(t *testing.T) {
	h := happy()
	h.tl.Roles["agent-5"], h.tl.Parent["agent-5"] = roster.DeepResearcher, mgr
	h.user(40, Research, "Do a deep-research pass on terminal mouse protocols").
		call(41, mgr, "create_agent", `{"input":{"role":"deep_researcher"}}`).
		call(42, mgr, "assign_task", `{}`).work(42, "work-3", work.DeepResearch, "agent-5", "", work.Active).
		call(43, "agent-5", "deep_research", `{}`).
		work(50, "work-3", work.DeepResearch, "agent-5", work.Active, work.Delivered).
		msg(51, mgr, message.User, message.Reply, "Here is the research.")
	if v := h.tl.Violations(true); len(v) > 0 {
		t.Fatalf("requested deep research rejected:\n%s", strings.Join(v, "\n"))
	}
}

// An experiment is assigned, delivers its conclusion, and the manager reports.
func TestMachineAcceptsAnExperiment(t *testing.T) {
	h := happy()
	h.tl.Roles["agent-6"], h.tl.Parent["agent-6"] = roster.Experimenter, mgr
	h.user(40, Research, "Is rendering slow?").
		call(41, mgr, "create_agent", `{"input":{"role":"experimenter"}}`).
		call(42, mgr, "assign_task", `{}`).work(42, "work-6", work.Experiment, "agent-6", "", work.Active).
		work(60, "work-6", work.Experiment, "agent-6", work.Active, work.Delivered).
		msg(61, mgr, message.User, message.Reply, "Rendering is fast; here are the numbers.")
	if v := h.tl.Violations(true); len(v) > 0 {
		t.Fatalf("experiment rejected:\n%s", strings.Join(v, "\n"))
	}
	if paths := h.tl.Paths(); !strings.Contains(paths, "new → active → delivered") {
		t.Fatal(paths)
	}
}

// A review scoped to a plan step completes it when delivered.
func TestMachineAcceptsDeliveredReviewCompletingItsStep(t *testing.T) {
	h := newHistory()
	rev := identity.ActorID("agent-7")
	h.tl.Roles[rev], h.tl.Parent[rev] = roster.Reviewer, mgr
	h.user(0, Research, "What does the parser do?").
		call(1, mgr, "create_plan", `{}`).step(1, "", work.Pending).
		call(2, mgr, "create_agent", `{"input":{"role":"reviewer"}}`).
		call(3, mgr, "assign_task", `{}`).work(3, "work-7", work.Review, rev, "", work.Active)
	w := h.tl.Works["work-7"]
	w.Scope = &work.Scope{PlanID: "plan-1", StepIDs: []work.StepID{"step-1"}}
	h.tl.Works["work-7"] = w
	h.work(9, "work-7", work.Review, rev, work.Active, work.Delivered).step(9, work.Pending, work.Completed).
		msg(10, mgr, message.User, message.Reply, "It tokenizes, then builds a tree.")
	if v := h.tl.Violations(true); len(v) > 0 {
		t.Fatalf("delivered review completing its step rejected:\n%s", strings.Join(v, "\n"))
	}
}

// reviewed runs the happy task up to its first audit, then lets end finish it.
func reviewed(end func(h *history)) *history {
	h := newHistory().
		user(0, Task, "Implement it").
		call(10, mgr, "create_plan", `{}`).step(10, "", work.Pending).
		call(11, mgr, "create_agent", `{"input":{"role":"implementor"}}`).
		call(12, mgr, "assign_task", `{}`).work(12, "work-1", work.Implementation, impl, "", work.Active).
		call(13, mgr, "wait_for_input", `{}`).write(15, impl, "text.go").
		step(20, work.Pending, work.ReadyForReview).work(21, "work-1", work.Implementation, impl, work.Active, work.NeedsCheck).
		call(22, mgr, "create_agent", `{"input":{"role":"auditor"}}`).
		call(23, mgr, "assign_audit", `{}`).work(23, "work-1", work.Implementation, impl, work.NeedsCheck, work.Checking).work(23, "work-2", work.AuditWork, aud, "", work.Active)
	end(h)
	return h.msg(41, mgr, message.User, message.Reply, "Done and audited.")
}

// An auditor that fails before its verdict has its audit cancelled, which
// returns the implementation to needs_check for a fresh audit (ladder
// easy-05, 2026-09-24).
func TestMachineAcceptsAReauditAfterACancelledAudit(t *testing.T) {
	h := reviewed(func(h *history) {
		h.work(25, "work-2", work.AuditWork, aud, work.Active, work.Cancelled).
			work(25, "work-1", work.Implementation, impl, work.Checking, work.NeedsCheck).
			call(26, mgr, "assign_audit", `{}`).work(26, "work-1", work.Implementation, impl, work.NeedsCheck, work.Checking).
			work(26, "work-3", work.AuditWork, aud, "", work.Active).
			work(30, "work-1", work.Implementation, impl, work.Checking, work.Accepted).work(30, "work-3", work.AuditWork, aud, work.Active, work.Closed).
			step(30, work.ReadyForReview, work.Completed)
	})
	if v := h.tl.Violations(true); len(v) > 0 {
		t.Fatalf("re-audit after a cancelled audit rejected:\n%s", strings.Join(v, "\n"))
	}
}

// A repair rewrites the file its implementation wrote; the implementation
// waits in changes_requested, so the two are one writer (ladder medium-09
// and medium-19, 2026-09-24).
func TestMachineAcceptsARepairRewritingItsImplementation(t *testing.T) {
	h := reviewed(func(h *history) {
		h.work(25, "work-1", work.Implementation, impl, work.Checking, work.ChangesRequested).work(25, "work-2", work.AuditWork, aud, work.Active, work.Closed).
			step(25, work.ReadyForReview, work.InProgress).
			call(26, mgr, "assign_repair", `{}`).work(26, "work-3", work.Repair, impl, "", work.Active).write(27, impl, "text.go").
			step(28, work.InProgress, work.ReadyForReview).
			work(28, "work-3", work.Repair, impl, work.Active, work.Closed).work(28, "work-1", work.Implementation, impl, work.ChangesRequested, work.NeedsCheck).
			call(29, mgr, "assign_audit", `{}`).work(29, "work-1", work.Implementation, impl, work.NeedsCheck, work.Checking).
			work(29, "work-4", work.AuditWork, aud, "", work.Active).
			work(35, "work-1", work.Implementation, impl, work.Checking, work.Accepted).work(35, "work-4", work.AuditWork, aud, work.Active, work.Closed).
			step(35, work.ReadyForReview, work.Completed)
	})
	if v := h.tl.Violations(true); len(v) > 0 {
		t.Fatalf("repair rewriting its implementation rejected:\n%s", strings.Join(v, "\n"))
	}
}

// A status question asked while work runs is answered before the task it
// asked about; both reports are due (live scenario "machine", 2026-09-23).
func TestMachineAcceptsAStatusAnswerDuringWork(t *testing.T) {
	h := happy().
		user(16, Question, "How is it going?").
		msg(18, mgr, message.User, message.Reply, "In progress: the implementor is on it.")
	sort.SliceStable(h.tl.Messages, func(i, j int) bool { return h.tl.Messages[i].At < h.tl.Messages[j].At })
	sort.SliceStable(h.tl.Calls, func(i, j int) bool { return h.tl.Calls[i].At < h.tl.Calls[j].At })
	sort.SliceStable(h.tl.Turns, func(i, j int) bool { return h.tl.Turns[i].At < h.tl.Turns[j].At })
	if v := h.tl.Violations(true); len(v) > 0 {
		t.Fatalf("status answer rejected:\n%s", strings.Join(v, "\n"))
	}
}

// Stopping everything at shutdown is the session ending, not a violation.
func TestMachineAcceptsShutdownStops(t *testing.T) {
	h := happy()
	h.tl.Exits = append(h.tl.Exits, Exit{At: sec(40), ID: mgr, State: "stopped"}, Exit{At: sec(40), ID: impl, State: "stopped"})
	if v := h.tl.Violations(true); len(v) > 0 {
		t.Fatalf("shutdown rejected:\n%s", strings.Join(v, "\n"))
	}
}
