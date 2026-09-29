package machine

// The harness as a state machine. A Timeline records what happened; this file
// states what is allowed to happen and checks the record against it.
//
//  0. Topology. A deterministic bootstrap function creates the session's one
//     manager, whose parent is the user, before any input; a debug session
//     also has a debugger beside it. Neither can be created again, stopped,
//     paused or resumed by any agent. The manager creates, stops, pauses and
//     resumes its own workers.
//
//     user ⇄ manager ⇄ {implementor, auditor, reviewer, web_researcher, deep_researcher, experimenter}
//
//     Each role holds one capability: the implementor alone changes the
//     workspace (1b), while auditors and experimenters change only their own
//     copies of it, the web researcher alone searches and reads the web,
//     and the deep researcher alone runs deep research, created only when a
//     user message asked for it.
//
//  1. Ledger machines. Every work item and plan step moves only along the
//     transitions below, whatever the model does.
//
//     implementation: new → active → needs_check → checking → accepted
//                                                  checking → changes_requested → needs_check (repair submitted)
//                                                  checking → needs_check (its audit cancelled)
//                     any live state → cancelled
//     repair, audit:  new → active → closed | cancelled
//     research,
//     experiment:     new → active → delivered | cancelled
//     step:           new → pending ⇄ in_progress ⇄ blocked → ready_for_review → completed
//                     ready_for_review → pending | in_progress (failed audit, cancelled work)
//                     any open status → cancelled
//     A step completes only when work whose scope covers it succeeds:
//     implementation accepted by an audit, research or an experiment delivered.
//
//  1b. Workspace. Every file change, named or made by a shell command, happens
//     under an implementation or repair its writer holds, and no file is
//     written under two live work items. A repair continues the implementation
//     it repairs, which waits in changes_requested meanwhile, so the two count
//     as one.
//
//  2. Conversation. The user talks to the manager. Every user message gets a
//     reply from the manager; a question or conversation is answered without
//     waiting on work, and an ambiguous change is answered with a question.
//     The user hears only replies.
//
//  3. Manager: persistent for the session. Every user message is answered by
//     at least one report, and there is no report without a new user message
//     since the last. The manager plans before it assigns implementation,
//     never changes the workspace itself, and reports only when its work is
//     finished or it needs a decision. Its first plan, assignment, review
//     request, audit and acceptance happen in that order, and audits go to an
//     auditor who did not implement the work.
//
//     idle ─user→ answer ─ report → idle
//     idle ─user→ plan → assign → wait → audit → (repair → audit)* → report → idle
//
//  4. End state. Once settled, no work is live, every step is completed or
//     cancelled, and every user message has its report.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/work"
)

var webTools = map[string]bool{"web_search": true, "open_url": true}

// deepResearchRequest matches a user message that asks for deep research.
var deepResearchRequest = regexp.MustCompile(`(?i)\bdeep[\s-]*(research|dive)`)

// askedForDeepResearch reports whether a user message before at asked for
// deep research.
func (tl Timeline) askedForDeepResearch(at time.Duration) bool {
	for _, t := range tl.Turns {
		if t.At <= at && deepResearchRequest.MatchString(t.Text) {
			return true
		}
	}
	return false
}

// createsRole reports whether a call's arguments, wrapped in the tool input
// envelope, create an agent of role: create_agent naming it, assign_task of
// the role's kind with a null assignee or the role's name, or assign_repair
// naming it as the assignee; both staff the work with a new agent.
func createsRole(name, args string, role roster.Role) bool {
	var call struct {
		Input struct {
			Role     roster.Role  `json:"role"`
			Kind     work.Kind    `json:"kind"`
			Assignee *roster.Role `json:"assignee"`
		} `json:"input"`
	}
	if json.Unmarshal([]byte(args), &call) != nil {
		return false
	}
	switch name {
	case "create_agent":
		return call.Input.Role == role
	case "assign_task":
		return roster.RoleFor(call.Input.Kind) == role && (call.Input.Assignee == nil || *call.Input.Assignee == role)
	case "assign_repair":
		return call.Input.Assignee != nil && *call.Input.Assignee == role
	}
	return false
}

var workMachine = map[work.Kind]map[work.State][]work.State{
	work.Implementation: {
		"":                    {work.Active},
		work.Active:           {work.NeedsCheck, work.Cancelled},
		work.NeedsCheck:       {work.Checking, work.Cancelled},
		work.Checking:         {work.Accepted, work.ChangesRequested, work.Cancelled},
		work.ChangesRequested: {work.NeedsCheck, work.Cancelled},
	},
	work.Repair:       {"": {work.Active}, work.Active: {work.Closed, work.Cancelled}},
	work.AuditWork:    {"": {work.Active}, work.Active: {work.Closed, work.Cancelled}},
	work.Review:       {"": {work.Active}, work.Active: {work.Delivered, work.Cancelled}},
	work.WebResearch:  {"": {work.Active}, work.Active: {work.Delivered, work.Cancelled}},
	work.DeepResearch: {"": {work.Active}, work.Active: {work.Delivered, work.Cancelled}},
	work.Experiment:   {"": {work.Active}, work.Active: {work.Delivered, work.Cancelled}},
}

var stepMachine = map[work.StepStatus][]work.StepStatus{
	"": {work.Pending},
	// An open step completes directly when a brief or an experiment scoped
	// to it is delivered; implementation steps pass through ready_for_review,
	// which the coverage rule below checks.
	work.Pending:        {work.InProgress, work.Blocked, work.ReadyForReview, work.CancelledStep, work.Completed},
	work.InProgress:     {work.Pending, work.Blocked, work.ReadyForReview, work.CancelledStep, work.Completed},
	work.Blocked:        {work.Pending, work.InProgress, work.ReadyForReview, work.CancelledStep, work.Completed},
	work.ReadyForReview: {work.Completed, work.Pending, work.InProgress, work.CancelledStep},
}

func terminal(s work.State) bool {
	return s == work.Accepted || s == work.Closed || s == work.Cancelled || s == work.Delivered
}

// managerPhase abstracts a manager's successful call into a protocol phase.
func managerPhase(name string) string {
	switch name {
	case "create_plan", "add_step", "edit_step", "cancel_steps", "reorder_steps", "rename_plan":
		return "planning"
	case "create_agent":
		return "staffing"
	case "assign_task", "assign_audit", "assign_repair", "cancel_work", "reassign_work":
		return "assigning"
	case "write_file", "edit_file", "shell", "report_work_progress", "submit_work":
		return "implementing"
	case "wait_for_input":
		return "waiting"
	case "send_message":
		return "messaging"
	}
	return "reading"
}

func asksOrBlocks(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(text, "?") || strings.Contains(lower, "blocked") || strings.Contains(lower, "blocker") || strings.Contains(lower, "decision")
}

// violations checks the whole timeline against the machine and returns one
// line per broken rule, each prefixed with the rule's layer.
func (tl Timeline) Violations(settled bool) []string {
	var v []string
	bad := func(format string, args ...any) { v = append(v, fmt.Sprintf(format, args...)) }

	// 0. Topology.
	if m := tl.Managers(); len(m) != 1 || tl.Parent[m[0]] != message.User {
		bad("topology: want exactly one manager under the user, got %v", m)
	}
	// The topology the harness enforces, rebuilt from the registrations: every
	// message must travel the edge its kind needs.
	graph := roster.NewGraph()
	for _, r := range tl.Registrations {
		if err := graph.Add(r); err != nil {
			bad("topology: registration %s (%s under %s) breaks the graph: %v", r.AgentID, r.Role, r.Parent, err)
		}
	}
	if len(tl.Registrations) > 0 {
		for _, msg := range tl.Messages {
			if msg.From == msg.To || msg.Kind == message.Observation {
				continue // Host notices to the owner itself.
			}
			if msg.To == message.User && msg.Kind != message.Reply && msg.Kind != message.Failure {
				bad("topology: %s sent the user a %s at %s; the user hears only replies", graph.Describe(msg.From), msg.Kind, msg.At)
			}
			if edge := roster.EdgeFor(msg.Kind); !graph.Allows(msg.From, msg.To, edge) {
				bad("topology: %s sent %s a %s at %s with no %s edge", graph.Describe(msg.From), graph.Describe(msg.To), msg.Kind, msg.At, edge)
			}
		}
	}
	for _, c := range tl.Calls {
		// The web belongs to researchers: what the session learns from it
		// arrives as research with recorded claims and sources.
		if webTools[c.Name] && tl.Roles[c.Agent] != roster.WebResearcher && tl.Roles[c.Agent] != roster.Agent {
			bad("topology: %s (%s) called %s at %s; only web researchers and a solo agent use the web", c.Agent, tl.Roles[c.Agent], c.Name, c.Start)
		}
		if c.Name == "deep_research" && tl.Roles[c.Agent] != roster.DeepResearcher {
			bad("topology: %s (%s) called deep_research at %s; only a deep researcher runs it", c.Agent, tl.Roles[c.Agent], c.Start)
		}
		// Deep research runs only when the user asked for it.
		if c.Err == "" && createsRole(c.Name, c.Args, roster.DeepResearcher) && !tl.askedForDeepResearch(c.Start) {
			bad("topology: %s created a deep researcher at %s, but no user message before it asked for deep research", c.Agent, c.Start)
		}
	}

	// The bootstrap's agents live as long as the session: nothing may stop them
	// while it goes on. Stops at shutdown, with nothing after them, are the
	// session ending.
	for _, e := range tl.Exits {
		if tl.Roles[e.ID] != roster.Manager && tl.Roles[e.ID] != roster.Agent {
			continue
		}
		after := false
		for _, msg := range tl.Messages {
			after = after || msg.At > e.At+time.Second
		}
		for _, c := range tl.Calls {
			after = after || c.Start > e.At+time.Second
		}
		if after {
			bad("topology: %s (%s) %s at %s while the session went on; the manager lives as long as the session", e.ID, tl.Roles[e.ID], e.State, e.At)
		}
	}

	// 1. Ledger machines.
	// Cancelling an audit returns its implementation to needs_check so the
	// submission can be audited again; nothing else may.
	auditCancelled := map[work.ID][]time.Duration{}
	for _, c := range tl.WorkLog {
		if c.Kind == work.AuditWork && c.To == work.Cancelled {
			auditCancelled[c.Parent] = append(auditCancelled[c.Parent], c.At)
		}
	}
	last := map[work.ID]work.State{}
	checkingSince := map[work.ID]time.Duration{}
	for _, c := range tl.WorkLog {
		if prior, ok := last[c.ID]; ok && prior != c.From {
			bad("ledger: %s recorded from %q but was %q", c.ID, c.From, prior)
		}
		if c.To == work.Checking {
			checkingSince[c.ID] = c.At
		}
		reaudit := c.Kind == work.Implementation && c.From == work.Checking && c.To == work.NeedsCheck &&
			slices.ContainsFunc(auditCancelled[c.ID], func(at time.Duration) bool { return at >= checkingSince[c.ID] && at <= c.At })
		if !reaudit && !slices.Contains(workMachine[c.Kind][c.From], c.To) {
			bad("ledger: %s %s moved %q → %s (%s), which the machine does not allow", c.Kind, c.ID, c.From, c.To, c.Event)
		}
		last[c.ID] = c.To
	}
	for _, c := range tl.StepLog {
		if !slices.Contains(stepMachine[c.From], c.To) {
			bad("ledger: step %s moved %q → %s, which the machine does not allow", c.Step, c.From, c.To)
		}
		if c.To != work.Completed {
			continue
		}
		// Work completes the steps it covers in its own success state:
		// implementation when an audit accepts it, from ready_for_review;
		// research and experiments when delivered.
		covered := false
		for id, w := range tl.StateAt(c.At) {
			full := tl.Works[id]
			if full.Scope == nil || !slices.Contains(full.Scope.StepIDs, c.Step) {
				continue
			}
			switch {
			case w.Kind == work.Implementation && w.To == work.Accepted && c.From == work.ReadyForReview:
				covered = true
			case (w.Kind.Investigation() || w.Kind == work.Experiment) && w.To == work.Delivered:
				covered = true
			}
		}
		if !covered {
			bad("ledger: step %s completed at %s without accepted or delivered work covering it", c.Step, c.At)
		}
	}

	// 1b. Workspace. Every write happens under an implementation or repair
	// its writer holds, and no file is written under two work items that
	// are both live: two writers on one file is a race whoever wins. A write
	// under a repair is held by the implementation it repairs, which is
	// waiting on that repair and cannot race it.
	holder := map[Write]work.ID{}
	for _, w := range tl.Writes {
		if tl.Roles[w.Agent] == roster.Agent {
			continue // A solo agent is the session's one writer and holds no work.
		}
		if role := tl.Roles[w.Agent]; role != roster.Implementor {
			bad("workspace: %s (%s) wrote %s with %s at %s; only implementors change the workspace", w.Agent, role, w.Path, w.Tool, w.At)
			continue
		}
		var held work.ID
		for id, c := range tl.StateAt(w.At) {
			if c.Assignee == w.Agent && c.To == work.Active && c.Kind == work.Implementation {
				held = id
			}
			if c.Assignee == w.Agent && c.To == work.Active && c.Kind == work.Repair {
				held = c.Parent
			}
		}
		if held == "" {
			bad("workspace: %s (%s) wrote %s with %s at %s without an active implementation or repair", w.Agent, tl.Roles[w.Agent], w.Path, w.Tool, w.At)
			continue
		}
		holder[w] = held
	}
	reported := map[[2]work.ID]bool{}
	for a, ida := range holder {
		for b, idb := range holder {
			if a.Path != b.Path || ida >= idb || reported[[2]work.ID{ida, idb}] {
				continue
			}
			later := max(a.At, b.At)
			state := tl.StateAt(later)
			if !terminal(state[ida].To) && !terminal(state[idb].To) {
				reported[[2]work.ID{ida, idb}] = true
				bad("workspace: %s written under %s (%s) and %s (%s) while both were live", a.Path, ida, a.Agent, idb, b.Agent)
			}
		}
	}

	// 2. Conversation: every user message gets the manager's reply.
	replies := tl.RepliesToUser(0)
	for i, turn := range tl.Turns {
		var first *Message
		for j, r := range replies {
			if r.At > turn.At {
				first = &replies[j]
				break
			}
		}
		if first == nil {
			bad("conversation: no reply to the %s at %s (%q)", turn.Intent, turn.At, Clip(turn.Text, 60))
			continue
		}
		if wait := first.At - turn.At; (turn.Intent == Question || turn.Intent == Chat) && wait > 2*time.Minute {
			bad("conversation: took %s to answer the %s at %s; questions and conversation must not wait on work", wait.Round(time.Second), turn.Intent, turn.At)
		}
		if turn.Intent == Ambiguous {
			next := time.Duration(1<<62 - 1)
			if i+1 < len(tl.Turns) {
				next = tl.Turns[i+1].At
			}
			asked := false
			for _, r := range replies {
				asked = asked || r.At > turn.At && r.At < next && strings.Contains(r.Content, "?")
			}
			if !asked {
				bad("conversation: the ambiguous change at %s was not answered with a question", turn.At)
			}
		}
	}

	// 3. Manager protocol.
	for _, mgr := range tl.Managers() {
		if tl.Parent[mgr] != message.User {
			bad("manager: %s has parent %s, not the user", mgr, tl.Parent[mgr])
		}
		first := map[string]time.Duration{}
		mark := func(k string, at time.Duration) {
			if _, ok := first[k]; !ok {
				first[k] = at
			}
		}
		for _, c := range tl.Calls {
			if c.Agent != mgr || c.Err != "" {
				continue
			}
			if c.Name == "create_plan" {
				mark("plan", c.At)
			}
			if managerPhase(c.Name) == "implementing" {
				bad("manager: %s ran %s at %s; the manager plans and assigns, implementors change the workspace", mgr, c.Name, c.At)
			}
		}
		for _, c := range tl.WorkLog {
			if c.Owner != mgr {
				continue
			}
			switch {
			case c.Kind == work.Implementation && c.From == "":
				mark("assign", c.At)
				if full := tl.Works[c.ID]; full.Scope == nil {
					bad("manager: implementation %s has no plan scope", c.ID)
				}
			case c.To == work.NeedsCheck:
				mark("review", c.At)
			case c.Kind == work.AuditWork && c.From == "":
				mark("audit", c.At)
				implementer := tl.Works[c.Parent].Assignee
				if tl.Roles[c.Assignee] != roster.Auditor || c.Assignee == implementer {
					bad("manager: audit %s went to %s, not an independent auditor (implementer %s)", c.ID, c.Assignee, implementer)
				}
			case c.To == work.Accepted:
				mark("accepted", c.At)
			}
		}
		order := []string{"plan", "assign", "review", "audit", "accepted"}
		for i := 1; i < len(order); i++ {
			a, aok := first[order[i-1]]
			b, bok := first[order[i]]
			if bok && (!aok || b < a) {
				bad("manager: %s's first %s (%s) came before its first %s", mgr, order[i], b, order[i-1])
			}
		}
		var reports []Message
		for _, msg := range tl.Messages {
			if msg.From == mgr && msg.To == message.User && msg.Kind == message.Reply {
				reports = append(reports, msg)
			}
		}
		var instructions []Message
		for _, msg := range tl.Messages {
			if msg.From == message.User && msg.To == mgr && msg.Kind == message.Instruction {
				instructions = append(instructions, msg)
			}
		}
		// intent is what the user message behind an instruction asked.
		intent := func(instruction Message) Intent {
			kind := Unknown
			for _, turn := range tl.Turns {
				if turn.At <= instruction.At {
					kind = turn.Intent
				}
			}
			return kind
		}
		for i, r := range reports {
			// Instructions may be outstanding together, and a question is
			// answered before the task that preceded it; every report still
			// answers an instruction of its own.
			received := 0
			for _, in := range instructions {
				if in.At < r.At {
					received++
				}
			}
			if i+1 > received {
				bad("manager: %s reported at %s for the %d time with %d instructions received", mgr, r.At, i+1, received)
			}
			live := 0
			for _, w := range tl.StateAt(r.At) {
				if w.Owner == mgr && !terminal(w.To) {
					live++
				}
			}
			if live == 0 || asksOrBlocks(r.Content) {
				continue
			}
			// With work running, a report answers a question or conversation
			// that arrived since the last report, or it asks or names a blocker.
			since := time.Duration(-1)
			if i > 0 {
				since = reports[i-1].At
			}
			answering := false
			for _, in := range instructions {
				if k := intent(in); in.At > since && in.At < r.At && (k == Question || k == Chat) {
					answering = true
				}
			}
			if !answering {
				bad("manager: %s reported at %s with %d live work items and no question or blocker", mgr, r.At, live)
			}
		}
	}

	// A request for work is done by an implementor: before the manager
	// reports on one, it has assigned implementation, unless its report asks
	// the user something. A manager that wrote the code into its reply left
	// the workspace unchanged and the user told it was done. A request for
	// research is answered the same way, from delivered research or an
	// experiment: the manager has no web tools and runs nothing, so an answer
	// without one is its own recall.
	for i, turn := range tl.Turns {
		kinds, wanted := []work.Kind{work.Implementation, work.Repair}, "implementation"
		switch turn.Intent {
		case Task, FollowUp, Update:
		case Research:
			kinds, wanted = []work.Kind{work.Review, work.WebResearch, work.DeepResearch, work.Experiment}, "review, research or experiment"
		default:
			continue
		}
		next := time.Duration(1<<62 - 1)
		if i+1 < len(tl.Turns) {
			next = tl.Turns[i+1].At
		}
		for _, mgr := range tl.Managers() {
			if tl.Roles[mgr] == roster.Agent {
				continue // A solo agent does the work itself, not through work items.
			}
			var report *Message
			for j, msg := range tl.Messages {
				// A failure notice is the controller reporting the manager's
				// exit, not a report of the manager's own.
				if msg.From == mgr && msg.To == message.User && msg.Kind != message.Observation && msg.Kind != message.Failure && msg.At > turn.At && msg.At < next {
					report = &tl.Messages[j]
					break
				}
			}
			if report == nil || asksOrBlocks(report.Content) {
				continue
			}
			assigned := false
			for _, c := range tl.WorkLog {
				assigned = assigned || c.Owner == mgr && c.From == "" && slices.Contains(kinds, c.Kind) && c.At > turn.At && c.At < report.At
			}
			if !assigned {
				bad("manager: %s reported on the %s at %s at %s without assigning any %s", mgr, turn.Intent, turn.At, report.At, wanted)
			}
		}
	}

	// 4. End state.
	if settled {
		for id, w := range tl.StateAt(1<<62 - 1) {
			if !terminal(w.To) {
				bad("end: %s %s is still %s", w.Kind, id, w.To)
			}
		}
		for _, p := range tl.Plans {
			for _, s := range p.Steps {
				if s.Status != work.Completed && s.Status != work.CancelledStep {
					bad("end: step %s %q is %s", s.ID, Clip(s.Title, 50), s.Status)
				}
			}
		}
		for _, msg := range tl.Messages {
			if msg.From != message.User || msg.Kind != message.Instruction {
				continue
			}
			answered := false
			for _, r := range tl.Messages {
				answered = answered || r.From == msg.To && r.To == message.User && r.At > msg.At && (r.Kind == message.Reply || r.Kind == message.Failure)
			}
			if !answered {
				bad("end: %s never answered the user's message at %s (%q)", msg.To, msg.At, Clip(msg.Content, 60))
			}
		}
	}
	return v
}

// paths renders each agent's abstract state sequence, the machine as it ran.
func (tl Timeline) Paths() string {
	var b strings.Builder
	line := func(name string, states []string) {
		var compact []string
		for _, s := range states {
			if n := len(compact); n == 0 || !strings.HasPrefix(compact[n-1], s) {
				compact = append(compact, s)
			} else if !strings.HasSuffix(compact[n-1], "*") {
				compact[n-1] += "*"
			}
		}
		fmt.Fprintf(&b, "%-22s %s\n", name, strings.Join(compact, " → "))
	}
	// The manager's path shows its phases and the conversation: each user
	// message that reached it and each reply it sent the user.
	for _, mgr := range tl.Managers() {
		type step struct {
			at    time.Duration
			state string
		}
		var steps []step
		for _, c := range tl.Calls {
			if c.Agent == mgr && c.Err == "" {
				steps = append(steps, step{c.Start, managerPhase(c.Name)})
			}
		}
		for _, msg := range tl.Messages {
			switch {
			case msg.From == message.User && msg.To == mgr:
				steps = append(steps, step{msg.At, "user"})
			case msg.From == mgr && msg.To == message.User:
				steps = append(steps, step{msg.At, "reply"})
			}
		}
		sort.SliceStable(steps, func(i, j int) bool { return steps[i].at < steps[j].at })
		states := make([]string, len(steps))
		for i, st := range steps {
			states[i] = st.state
		}
		line(string(mgr)+"(manager)", states)
	}
	ids := make([]string, 0)
	byID := map[string][]string{}
	for _, c := range tl.WorkLog {
		key := fmt.Sprintf("%s(%s)", c.ID, c.Kind)
		if _, ok := byID[key]; !ok {
			ids = append(ids, key)
			byID[key] = []string{"new"}
		}
		byID[key] = append(byID[key], string(c.To))
	}
	for _, id := range ids {
		line(id, byID[id])
	}
	return b.String()
}
