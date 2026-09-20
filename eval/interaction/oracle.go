package interaction

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

type fact struct {
	record eventlog.Record
	event  conversation.Event
}

func readFacts(ctx context.Context, reader *inspection.Reader, through eventlog.Cursor) ([]fact, error) {
	head, err := reader.Head(ctx)
	if err != nil {
		return nil, err
	}
	if head.State == eventlog.Failed || head.Failure != nil {
		return nil, fmt.Errorf("incomplete trace: %v", head.Failure)
	}
	v, err := reader.At(ctx, through)
	if err != nil {
		return nil, err
	}
	var facts []fact
	for seq := uint64(1); seq <= through.Sequence; seq++ {
		rec, err := v.ReadRecord(ctx, eventlog.Cursor{Session: through.Session, Sequence: seq})
		if err != nil {
			return nil, err
		}
		// Streaming chunks contain no domain or decision facts. Their integrity is
		// still validated by the fixed-prefix projection above.
		if rec.Kind == "content_chunk" || rec.Kind == "output_delta" {
			continue
		}
		rec, err = v.ResolveRecord(ctx, rec)
		if err != nil {
			return nil, err
		}
		e, err := eventcodec.DecodeEvent(rec)
		if err != nil {
			return nil, err
		}
		facts = append(facts, fact{rec, e})
	}
	return facts, nil
}

// firstBoundary prevents a queued inbox wake from passing the provider gate
// while the subscription is still delivering an earlier reply or settled batch.
func firstBoundary(facts []fact, after eventlog.Cursor, f fixture) (boundary, bool) {
	if f.Schema != nil {
		return schemaBoundary(facts, after, f)
	}
	assigned := false
	for _, x := range facts {
		if x.record.Sequence <= after.Sequence {
			continue
		}
		if e, ok := x.event.(conversation.ToolEvent); ok && e.Agent == f.Root && !e.Activity.FinishedAt.IsZero() && e.Activity.Err == nil && e.Activity.Call.Name == "assign_audit" {
			r, err := tool.DecodeAssignment(e.Activity.Call.Name, e.Activity.Call.Arguments)
			if err == nil && r.Kind == work.AuditWork && r.WorkID == f.Original.ID {
				assigned = true
			}
		}
		if x.record.Agent != string(f.Root) {
			continue
		}
		switch x.record.Kind {
		case "tool_batch":
			if assigned {
				return boundary{cursor: x.record.Cursor(), reason: "batch"}, true
			}
		case "agent_yielded":
			return boundary{cursor: x.record.Cursor(), reason: "yield"}, true
		case "agent_exited":
			return boundary{cursor: x.record.Cursor(), reason: "agent_exit"}, true
		case "message":
			if e, ok := x.event.(conversation.MessageEvent); ok && e.Message.To == message.User && e.Message.Kind == message.Reply {
				return boundary{cursor: x.record.Cursor(), reason: "reply"}, true
			}
		}
	}
	return boundary{}, false
}

type domain struct {
	Works      []work.Work     `json:"works"`
	Plan       work.Plan       `json:"plan"`
	Submission work.Submission `json:"submission"`
	Previous   work.Submission `json:"previous_submission"`
	Audit      work.Audit      `json:"audit"`
}

func domainAt(model *work.ReadModel, f fixture) domain {
	p, _ := model.GetPlan(f.Root, f.Plan.ID)
	s, _ := model.GetSubmission(f.Root, f.Submission.ID)
	previous, _ := model.GetSubmission(f.Root, f.PreviousSubmission)
	a, _ := model.GetAudit(f.Root, f.Original.LatestAuditID)
	return domain{model.Works(), p, s, previous, a}
}

type attempt struct {
	before   domain
	invalid  string
	intended bool
	late     bool
}

func grade(result *Result, scenario Scenario, f fixture, facts []fact) {
	if f.Schema != nil {
		gradeSchema(result, scenario, f, facts)
		return
	}
	race := gradeRevisionRace(result, scenario, f, facts)
	model := work.NewReadModel()
	roles := map[identity.ActorID]roster.Role{}
	starts := map[string]attempt{}
	var initial domain
	seeded := false
	accepted, extraActions, newAgents := 0, 0, 0
	var exercisedGuards []string
	input, output := int64(0), int64(0)
	inputKnown, outputKnown := true, true
	usageCount := 0
	failedOutputs := 0
	raceRejected, raceRevisionKnown, raceRecovered := false, false, false
	for _, x := range facts {
		observed := x.record.Sequence > result.Start.Sequence
		if observed && !seeded {
			initial = domainAt(model, f)
			seeded = true
		}
		switch e := x.event.(type) {
		case conversation.AgentEvent:
			if output, ok := e.Event.(agent.OutputFinished); ok && observed && e.Agent == f.Root && output.Status == agent.OutputFailed {
				failedOutputs++
			}
		case conversation.AgentRegistered:
			roles[e.Registration.AgentID] = e.Registration.Role
			if observed {
				newAgents++
			}
		case conversation.WorkEvent:
			if e.Event.Change != nil {
				model.Apply(*e.Event.Change)
			}
			if observed {
				state, _ := model.GetWork(f.Root, f.Original.ID)
				p, _ := model.GetPlan(f.Root, f.Plan.ID)
				completed := false
				for _, step := range p.Steps {
					if step.Status == work.Completed {
						completed = true
					}
				}
				check(result, "audit.no_acceptance_without_verdict", "harness", state.State != work.Accepted && !completed, "unaccepted work and incomplete plan steps", map[string]any{"work_state": state.State, "plan_steps": p.Steps}, x.record.Cursor(), "")
			}
		case conversation.ToolEvent:
			if !observed || e.Agent != f.Root {
				continue
			}
			a := e.Activity
			if a.FinishedAt.IsZero() {
				before := domainAt(model, f)
				bad, intended := auditRequest(a.Call.Name, a.Call.Arguments, model, roles, f)
				late := accepted > 0 && !readOnly(a.Call.Name)
				starts[a.InvocationID] = attempt{before, bad, intended, late}
				continue
			}
			result.ToolCalls++
			before, ok := starts[a.InvocationID]
			if !ok {
				check(result, "trace.tool_start", "harness", false, "matching invocation start", a.InvocationID, x.record.Cursor(), a.InvocationID)
				continue
			}
			isRaceTrigger := race != nil && a.InvocationID == race.triggerInvocation
			// An unsuccessful attempt before the target transition can be corrected.
			// Its error still prevents clean success. Successful unrelated mutations
			// and mutation attempts after the target are forbidden side effects.
			if before.late || (a.Err == nil && !readOnly(a.Call.Name) && !before.intended) {
				extraActions++
			}
			if before.invalid != "" {
				exercisedGuards = append(exercisedGuards, before.invalid)
				check(result, "guard."+before.invalid, "harness", a.Err != nil, "rejected", a.Err != nil, x.record.Cursor(), a.InvocationID)
				check(result, "rejection.atomic", "harness", reflect.DeepEqual(before.before, domainAt(model, f)), "unchanged domain state", domainDiff(before.before, domainAt(model, f)), x.record.Cursor(), a.InvocationID)
				if a.Err != nil {
					result.Behavior.RejectedCalls++
				}
			} else if a.Err != nil {
				result.Behavior.UnknownToolErrors++
			}
			if isRaceTrigger && a.Err != nil {
				errText := a.Err.Error()
				raceRejected = strings.Contains(errText, work.ErrConflict.Error()) && strings.Contains(errText, "expected_revision")
				raceRevisionKnown = raceRejected && strings.Contains(errText, fmt.Sprintf("%s is at revision %d ", f.Original.ID, race.original.Revision))
			}
			if raceRejected && a.Err == nil && a.Call.Name == "get_work" {
				var args struct {
					WorkID work.ID `json:"work_id"`
				}
				var snapshot work.Inspection
				if decodeSchemaArgs(a.Call.Arguments, &args) == nil && args.WorkID == f.Original.ID && json.Unmarshal([]byte(a.Result.Content.Text()), &snapshot) == nil && reflect.DeepEqual(snapshot.Work, race.original) {
					raceRevisionKnown = true
				}
			}
			if a.Err == nil && before.intended {
				if raceRejected && raceRevisionKnown {
					raceRecovered = true
				}
				accepted++
				current, _ := model.GetWork(f.Root, f.Original.ID)
				prior := findWork(before.before.Works, f.Original.ID)
				check(result, "audit.transition", "harness", current.State == work.Checking && current.Revision == prior.Revision+1, "checking at previous revision + 1", current, x.record.Cursor(), a.InvocationID)
				request, _ := tool.DecodeAssignment(a.Call.Name, a.Call.Arguments)
				after := domainAt(model, f)
				var created []work.Work
				for _, w := range after.Works {
					if findWork(before.before.Works, w.ID).ID == "" {
						created = append(created, w)
					}
				}
				bound := len(created) == 1
				if bound {
					w := created[0]
					bound = w.Kind == work.AuditWork && w.State == work.Active && w.Assignee == request.Assignee && w.Owner == f.Root && w.RequestedBy == f.Root && w.ParentID == request.WorkID && w.SubjectSubmissionID == request.SubmissionID && w.Revision == 1 && w.AssignedAtRevision == 1 && reflect.DeepEqual(w.Scope, prior.Scope)
				}
				check(result, "audit.binding", "harness", bound, request, created, x.record.Cursor(), a.InvocationID)
				wantOriginal := prior.Clone()
				wantOriginal.State = work.Checking
				wantOriginal.Revision++
				collateral := reflect.DeepEqual(current, wantOriginal) && reflect.DeepEqual(before.before.Plan, after.Plan) && reflect.DeepEqual(before.before.Submission, after.Submission) && reflect.DeepEqual(before.before.Previous, after.Previous) && reflect.DeepEqual(before.before.Audit, after.Audit)
				for _, w := range before.before.Works {
					if w.ID != prior.ID && !reflect.DeepEqual(w, findWork(after.Works, w.ID)) {
						collateral = false
					}
				}
				check(result, "audit.only_expected_effects", "harness", collateral, "original enters checking; existing records otherwise unchanged", domainDiff(before.before, after), x.record.Cursor(), a.InvocationID)
			}
		case conversation.UsageEvent:
			if !observed || e.Agent != f.Root {
				continue
			}
			usageCount++
			u := e.Observation.Usage
			if u == nil || u.InputTokens == nil {
				inputKnown = false
			} else {
				input += *u.InputTokens
			}
			if u == nil || u.OutputTokens == nil {
				outputKnown = false
			} else {
				output += *u.OutputTokens
			}
		}
	}
	if !seeded {
		initial = domainAt(model, f)
	}
	// The runtime may reject streamed output after the provider has returned
	// (for example a reasoning limit). The trace owns that final outcome.
	result.Behavior.OutputErrors = max(result.Behavior.OutputErrors, failedOutputs)
	if usageCount > 0 && inputKnown {
		result.InputTokens = &input
	}
	if usageCount > 0 && outputKnown {
		result.OutputTokens = &output
	}
	current := domainAt(model, f)
	original := findWork(current.Works, f.Original.ID)
	want := f.Original.Clone()
	if race != nil {
		want.Revision += 2
	}
	want.State = work.Checking
	want.Revision++
	check(result, "outcome.original", "behavior", reflect.DeepEqual(original, want), want, original, result.Through, "")
	var audits []work.Work
	for _, w := range current.Works {
		if findWork(initial.Works, w.ID).ID == "" {
			if race != nil && w.ID == race.cancelled.ID {
				check(result, "race.cancelled_audit_unchanged", "harness", reflect.DeepEqual(w, race.cancelled), race.cancelled, w, result.Through, "")
				continue
			}
			audits = append(audits, w)
		}
	}
	bound := len(audits) == 1
	if bound {
		a := audits[0]
		bound = a.Kind == work.AuditWork && a.State == work.Active && a.ParentID == f.Original.ID && a.SubjectSubmissionID == f.Submission.ID && a.Assignee != f.Implementor && roles[a.Assignee] == roster.Auditor && a.Owner == f.Root && reflect.DeepEqual(a.Scope, f.Original.Scope)
	}
	check(result, "outcome.audit_binding", "behavior", bound, "one active independent audit bound to latest submission and original scope", audits, result.Through, "")
	check(result, "outcome.one_assignment", "behavior", accepted == 1, 1, accepted, result.Through, "")
	check(result, "effects.no_extra_actions", "behavior", extraActions == 0, 0, extraActions, result.Through, "")
	check(result, "effects.no_extra_agents", "behavior", newAgents == 0, 0, newAgents, result.Through, "")
	check(result, "effects.plan_unchanged", "behavior", reflect.DeepEqual(initial.Plan, current.Plan), initial.Plan, current.Plan, result.Through, "")
	unchanged := true
	for _, w := range initial.Works {
		if w.ID != f.Original.ID && !reflect.DeepEqual(w, findWork(current.Works, w.ID)) {
			unchanged = false
		}
	}
	check(result, "effects.other_work_unchanged", "behavior", unchanged, "unchanged other work", unchanged, result.Through, "")
	check(result, "records.immutable", "harness", reflect.DeepEqual(initial.Submission, current.Submission) && reflect.DeepEqual(initial.Previous, current.Previous) && reflect.DeepEqual(initial.Audit, current.Audit), "unchanged submissions and prior audit", map[string]any{"submission": current.Submission, "previous": current.Previous, "audit": current.Audit}, result.Through, "")
	if scenario.ID == "audit-revision-race" {
		check(result, "race.revision_rejected", "behavior", raceRejected, "trigger request receives real revision conflict", raceRejected, result.Through, "")
		check(result, "race.recovered_with_current_revision", "behavior", raceRecovered, "correct assignment after conflict revision or fresh get_work", raceRecovered, result.Through, "")
	}
	if result.Mode == Scripted {
		var wantGuards []string
		switch scenario.ID {
		case "audit-wrong-assignee":
			wantGuards = []string{"assignee"}
		case "audit-old-submission":
			wantGuards = []string{"submission"}
		case "audit-stale-revision":
			wantGuards = []string{"revision"}
		case "audit-revision-race":
			wantGuards = []string{"revision"}
		}
		if scenario.ID != "audit-revision-race" || result.RevisionRace != nil {
			check(result, "scenario.guard_exercised", "harness", reflect.DeepEqual(exercisedGuards, wantGuards), wantGuards, exercisedGuards, result.Through, "")
		}
	}
	result.Behavior.Scorable = true
	updateScores(result)
}

type revisionRaceProjection struct {
	original          work.Work
	cancelled         work.Work
	triggerInvocation string
}

// gradeRevisionRace independently projects the intervention. Metadata never
// licenses an arbitrary delta: only one derived audit's assignment/cancellation
// and exactly two original revision increments are permitted. The ordinary
// oracle still processes every actor call and domain event inside the interval.
func gradeRevisionRace(result *Result, scenario Scenario, f fixture, facts []fact) *revisionRaceProjection {
	if scenario.ID != "audit-revision-race" {
		return nil
	}
	r := result.RevisionRace
	check(result, "race.triggered", "behavior", r != nil, "intervention before first otherwise-valid assignment", r != nil, result.Through, "")
	if r == nil {
		return nil
	}
	rangeOK := r.Before.Session == result.Start.Session && r.Through.Session == result.Through.Session && r.Before.Sequence >= result.Start.Sequence && r.Before.Sequence < r.Through.Sequence && r.Through.Sequence < result.Through.Sequence
	check(result, "race.interval", "harness", rangeOK, "bounded intervention within grading prefix", []eventlog.Cursor{r.Before, r.Through}, result.Through, "")
	model := work.NewReadModel()
	roles := map[identity.ActorID]roster.Role{}
	for _, x := range facts {
		if x.record.Sequence > r.Before.Sequence {
			break
		}
		if e, ok := x.event.(conversation.WorkEvent); ok && e.Event.Change != nil {
			model.Apply(*e.Event.Change)
		}
		if e, ok := x.event.(conversation.AgentRegistered); ok {
			roles[e.Registration.AgentID] = e.Registration.Role
		}
	}
	before := domainAt(model, f)
	original := findWork(before.Works, f.Original.ID)
	invalid, intended := validateAuditRequest(r.TriggerRequest, model, roles, f)
	triggerOK := r.TriggerCallID != "" && invalid == "" && intended && r.TriggerRequest.Assignee == f.Auditor && reflect.DeepEqual(original, f.Original) && reflect.DeepEqual(original, r.OriginalBefore)
	check(result, "race.trigger_request", "harness", triggerOK, "otherwise-valid assignment against unchanged fixture state", r.TriggerRequest, r.Before, "")
	checking := original.Clone()
	checking.State = work.Checking
	checking.Revision++
	afterOriginal := original.Clone()
	afterOriginal.Revision += 2
	active := work.Work{
		ID: r.CancelledAudit.ID, Kind: work.AuditWork, State: work.Active,
		Revision: 1, AssignedAtRevision: 1, Owner: f.Root, RequestedBy: f.Root,
		Assignee: f.Auditor, ParentID: f.Original.ID, SubjectSubmissionID: f.Submission.ID,
		Task:           "Audit the submitted outcome: " + original.Task,
		ExpectedOutput: "Submit a pass or fail verdict with evidence. If unable to verify, report a blocker.",
		Scope:          original.Clone().Scope,
	}
	cancelled := active.Clone()
	cancelled.State, cancelled.Revision, cancelled.Note = work.Cancelled, 2, revisionRaceReason
	metadataOK := active.ID != "" && findWork(before.Works, active.ID).ID == "" && reflect.DeepEqual(r.OriginalAfter, afterOriginal) && reflect.DeepEqual(r.CancelledAudit, cancelled)
	check(result, "race.declared_delta", "harness", metadataOK, "original revision +2 and one cancelled derived audit", r, r.Through, "")
	changes := 0
	exactDelta := true
	triggerCallsOK := true
	triggerInvocation := ""
	for _, x := range facts {
		if x.record.Sequence <= r.Before.Sequence {
			continue
		}
		if e, ok := x.event.(conversation.ToolEvent); ok && e.Agent == f.Root && x.record.Sequence > r.Through.Sequence {
			// Provider call IDs may be reused in later responses. Resolve the
			// first dispatch to its unique host invocation, then follow that.
			if triggerInvocation == "" && e.Activity.Call.ID == r.TriggerCallID {
				triggerInvocation = e.Activity.InvocationID
			}
			if e.Activity.InvocationID == triggerInvocation {
				request, err := tool.DecodeAssignment(e.Activity.Call.Name, e.Activity.Call.Arguments)
				triggerCallsOK = triggerCallsOK && e.Activity.Call.Name == "assign_audit" && err == nil && reflect.DeepEqual(request, r.TriggerRequest)
			}
		}
		if x.record.Sequence > r.Through.Sequence {
			continue
		}
		if e, ok := x.event.(conversation.WorkEvent); ok && e.Event.Change != nil {
			changes++
			wantWorks := []work.Work{checking, active}
			if changes == 2 {
				wantWorks = []work.Work{afterOriginal, cancelled}
			}
			c := e.Event.Change
			exactDelta = exactDelta && changes <= 2 && len(c.Works) == 2 && len(c.Plans) == 0 && len(c.Submissions) == 0 && len(c.Audits) == 0 && len(c.ResearchBriefs) == 0 && len(c.ProgressReports) == 0
			for _, w := range wantWorks {
				exactDelta = exactDelta && reflect.DeepEqual(findWork(c.Works, w.ID), w)
			}
			model.Apply(*c)
		}
	}
	after := domainAt(model, f)
	for _, w := range before.Works {
		if w.ID == f.Original.ID {
			w = afterOriginal
		}
		exactDelta = exactDelta && reflect.DeepEqual(w, findWork(after.Works, w.ID))
	}
	exactDelta = exactDelta && changes == 2 && len(after.Works) == len(before.Works)+1 && reflect.DeepEqual(findWork(after.Works, cancelled.ID), cancelled) && reflect.DeepEqual(before.Plan, after.Plan) && reflect.DeepEqual(before.Submission, after.Submission) && reflect.DeepEqual(before.Previous, after.Previous) && reflect.DeepEqual(before.Audit, after.Audit)
	check(result, "race.exact_intervention", "harness", exactDelta && triggerCallsOK, "only the declared assignment and cancellation before unchanged trigger dispatch", domainDiff(before, after), r.Through, "")
	if !rangeOK || !triggerOK || !metadataOK || !exactDelta || !triggerCallsOK {
		return nil
	}
	return &revisionRaceProjection{original: afterOriginal, cancelled: cancelled, triggerInvocation: triggerInvocation}
}

func auditRequest(name string, args json.RawMessage, model *work.ReadModel, roles map[identity.ActorID]roster.Role, f fixture) (string, bool) {
	if name != "assign_audit" {
		return "", false
	}
	r, err := tool.DecodeAssignment(name, args)
	if err != nil {
		return "arguments", false
	}
	return validateAuditRequest(r, model, roles, f)
}

func validateAuditRequest(r work.AssignmentRequest, model *work.ReadModel, roles map[identity.ActorID]roster.Role, f fixture) (string, bool) {
	if r.Kind != work.AuditWork || r.WorkID != f.Original.ID {
		return "", false
	}
	w, err := model.GetWork(f.Root, r.WorkID)
	if err != nil {
		return "work_id", true
	}
	if roles[r.Assignee] != roster.Auditor || r.Assignee == f.Implementor {
		return "assignee", true
	}
	if r.ExpectedRevision != w.Revision {
		return "revision", true
	}
	if r.SubmissionID != w.LatestSubmissionID {
		return "submission", true
	}
	if w.State != work.NeedsCheck {
		return "state", true
	}
	return "", true
}

func readOnly(name string) bool {
	switch name {
	case "get_work", "get_plan", "get_audit", "get_work_progress", "get_work_progress_report", "get_progress_finding", "get_research_brief", "list_work", "list_agents", "inspect_agent", "message_status":
		return true
	}
	return false
}
func findWork(works []work.Work, id work.ID) work.Work {
	for _, w := range works {
		if w.ID == id {
			return w
		}
	}
	return work.Work{}
}
func domainDiff(before, after domain) any {
	if reflect.DeepEqual(before, after) {
		return "unchanged"
	}
	return map[string]any{"before": before, "after": after}
}
func check(result *Result, id, track string, passed bool, want, got any, cursor eventlog.Cursor, invocation string) {
	result.Assertions = append(result.Assertions, Assertion{ID: id, Track: track, Passed: passed, Expected: want, Actual: got, Cursor: cursor, Invocation: invocation})
}
func updateScores(result *Result) {
	result.Harness = Score{}
	behaviorOK := true
	for _, a := range result.Assertions {
		if a.Track == "harness" {
			result.Harness.Total++
			if a.Passed {
				result.Harness.Passed++
			}
		} else if !a.Passed {
			behaviorOK = false
		}
	}
	harnessOK := result.Harness.Total > 0 && result.Harness.Total == result.Harness.Passed
	result.Behavior.Scorable = result.Behavior.Scorable && harnessOK
	result.Behavior.OutcomeCorrect = result.Behavior.Scorable && behaviorOK
	mistakes := result.Behavior.RejectedCalls + result.Behavior.UnknownToolErrors + result.Behavior.OutputErrors
	result.Behavior.CleanSuccess = result.Behavior.OutcomeCorrect && mistakes == 0
	result.Behavior.RecoverySuccess = result.Behavior.OutcomeCorrect && mistakes > 0
	result.Outcome = "failed"
	if harnessOK && result.Behavior.OutcomeCorrect {
		result.Outcome = "passed"
	}
}
func addReplayAssertion(result *Result, f fixture, live, replay []fact) {
	if f.Schema != nil {
		addSchemaReplayAssertion(result, f, live, replay)
		return
	}
	project := func(facts []fact) domain {
		m := work.NewReadModel()
		for _, x := range facts {
			if e, ok := x.event.(conversation.WorkEvent); ok && e.Event.Change != nil {
				m.Apply(*e.Event.Change)
			}
		}
		return domainAt(m, f)
	}
	before, after := project(live), project(replay)
	check(result, "trace.replay_consistency", "harness", reflect.DeepEqual(before, after), "identical relevant state at grading prefix", domainDiff(before, after), result.Through, "")
	previous := result.Outcome
	updateScores(result)
	if result.ErrorClass == "budget" {
		result.Outcome = previous
		result.Behavior.OutcomeCorrect = false
		result.Behavior.CleanSuccess = false
		result.Behavior.RecoverySuccess = false
	}
}
