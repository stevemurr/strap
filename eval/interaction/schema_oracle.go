package interaction

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/work"
)

// Keep every ledger record, including records not reachable from the fixture's
// current work pointers. A rejected call must not leave an orphan report behind.
type schemaLedger struct {
	Works       map[work.ID]work.Work
	Plans       map[work.PlanID]work.Plan
	Submissions map[work.SubmissionID]work.Submission
	Audits      map[work.AuditID]work.Audit
	Briefs      map[work.ResearchBriefID]work.ResearchBrief
	Reports     map[work.ProgressReportID]work.WorkProgressReport
}

func newSchemaLedger() schemaLedger {
	return schemaLedger{
		Works: map[work.ID]work.Work{}, Plans: map[work.PlanID]work.Plan{},
		Submissions: map[work.SubmissionID]work.Submission{}, Audits: map[work.AuditID]work.Audit{},
		Briefs: map[work.ResearchBriefID]work.ResearchBrief{}, Reports: map[work.ProgressReportID]work.WorkProgressReport{},
	}
}

func (s schemaLedger) clone() schemaLedger {
	return schemaLedger{maps.Clone(s.Works), maps.Clone(s.Plans), maps.Clone(s.Submissions), maps.Clone(s.Audits), maps.Clone(s.Briefs), maps.Clone(s.Reports)}
}

func (s schemaLedger) apply(c work.Change) {
	for _, v := range c.Works {
		s.Works[v.ID] = v.Clone()
	}
	for _, v := range c.Plans {
		s.Plans[v.ID] = v.Clone()
	}
	for _, v := range c.Submissions {
		s.Submissions[v.ID] = v.Clone()
	}
	for _, v := range c.Audits {
		s.Audits[v.ID] = v.Clone()
	}
	for _, v := range c.ResearchBriefs {
		s.Briefs[v.ID] = v.Clone()
	}
	for _, v := range c.ProgressReports {
		s.Reports[v.ID] = v.Clone()
	}
}

func schemaBoundary(facts []fact, after eventlog.Cursor, f fixture) (boundary, bool) {
	accepted := false
	for _, x := range facts {
		if x.record.Sequence <= after.Sequence {
			continue
		}
		if e, ok := x.event.(conversation.ToolEvent); ok && e.Agent == f.actor() && !e.Activity.FinishedAt.IsZero() && e.Activity.Err == nil && e.Activity.Call.Name == f.Schema.Operation {
			accepted = true
		}
		if x.record.Agent != string(f.actor()) {
			continue
		}
		switch x.record.Kind {
		case "tool_batch":
			if accepted {
				return boundary{cursor: x.record.Cursor(), reason: "batch"}, true
			}
		case "agent_yielded":
			return boundary{cursor: x.record.Cursor(), reason: "yield"}, true
		case "agent_exited":
			return boundary{cursor: x.record.Cursor(), reason: "agent_exit"}, true
		case "message":
			if e, ok := x.event.(conversation.MessageEvent); ok && e.Message.From == f.actor() && e.Message.Kind == message.Reply {
				return boundary{cursor: x.record.Cursor(), reason: "reply"}, true
			}
		}
	}
	return boundary{}, false
}

func compileSchemaCatalog(definitions []provider.ToolDefinition) (map[string]*jsonschema.Schema, error) {
	out := map[string]*jsonschema.Schema{}
	for _, definition := range definitions {
		if _, duplicate := out[definition.Name]; duplicate {
			return nil, fmt.Errorf("duplicate tool %s", definition.Name)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(definition.Parameters))
		if err != nil {
			return nil, fmt.Errorf("%s schema: %w", definition.Name, err)
		}
		compiler := jsonschema.NewCompiler()
		compiler.DefaultDraft(jsonschema.Draft2020)
		const location = "https://strap.test/interaction.schema.json"
		if err := compiler.AddResource(location, doc); err != nil {
			return nil, err
		}
		schema, err := compiler.Compile(location)
		if err != nil {
			return nil, fmt.Errorf("%s schema: %w", definition.Name, err)
		}
		out[definition.Name] = schema
	}
	return out, nil
}

func schemaArgumentsValid(schema *jsonschema.Schema, raw json.RawMessage) bool {
	if schema == nil {
		return false
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	return err == nil && schema.Validate(v) == nil
}

// JSON Schema integers include 7.0 and 7e0. Normalize exact numeric values
// independently of the production decoder before projecting typed commands.
func decodeSchemaArgs(raw json.RawMessage, target any) error {
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	var canonicalize func(any) any
	canonicalize = func(value any) any {
		switch v := value.(type) {
		case json.Number:
			if number, ok := new(big.Rat).SetString(string(v)); ok && number.IsInt() {
				return json.Number(number.Num().String())
			}
		case map[string]any:
			for key, child := range v {
				v[key] = canonicalize(child)
			}
		case []any:
			for i, child := range v {
				v[i] = canonicalize(child)
			}
		}
		return value
	}
	encoded, err := json.Marshal(canonicalize(value))
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

type schemaAttempt struct {
	before                              schemaLedger
	changeIndex                         int
	known, valid, intended, late, probe bool
	errorNeedle                         string
}

func gradeSchema(result *Result, scenario Scenario, f fixture, facts []fact) {
	result.Schema = &SchemaBehavior{}
	catalog, err := compileSchemaCatalog(f.Schema.Tools)
	check(result, "schema.catalog", "harness", err == nil && catalog[f.Schema.Operation] != nil && catalog["assign_work"] == nil, "actual actor schemas compile; expected operation exists; removed operation absent", fmt.Sprint(err), result.Through, "")
	if err != nil {
		updateScores(result)
		return
	}
	state := newSchemaLedger()
	var initial, expected schemaLedger
	seeded := false
	starts := map[string]schemaAttempt{}
	mixedControl := schemaMixedControlInvocations(f, facts, catalog["wait_for_input"] != nil)
	var changes []work.Change
	accepted, extraActions, newAgents, probes, failedOutputs := 0, 0, 0, 0, 0
	input, output := int64(0), int64(0)
	inputKnown, outputKnown, usageCount := true, true, 0
	for _, x := range facts {
		observed := x.record.Sequence > result.Start.Sequence
		if observed && !seeded {
			initial, expected, seeded = state.clone(), state.clone(), true
		}
		switch e := x.event.(type) {
		case conversation.AgentRegistered:
			if observed {
				newAgents++
			}
		case conversation.AgentEvent:
			if out, ok := e.Event.(agent.OutputFinished); ok && observed && e.Agent == f.actor() && out.Status == agent.OutputFailed {
				failedOutputs++
			}
		case conversation.WorkEvent:
			if e.Event.Change != nil {
				state.apply(*e.Event.Change)
				if observed {
					changes = append(changes, e.Event.Change.Clone())
				}
			}
		case conversation.ToolEvent:
			if !observed || e.Agent != f.actor() {
				continue
			}
			a := e.Activity
			if a.FinishedAt.IsZero() {
				known := catalog[a.Call.Name] != nil
				valid := schemaArgumentsValid(catalog[a.Call.Name], a.Call.Arguments)
				probe, needle := schemaProbe(scenario.ID, a.Call.Name, a.Call.Arguments)
				starts[a.InvocationID] = schemaAttempt{state.clone(), len(changes), known, valid, valid && schemaIntended(f, a.Call), accepted > 0 && !readOnly(a.Call.Name), probe, needle}
				if !readOnly(a.Call.Name) {
					result.Schema.Attempts++
					if !valid {
						result.Schema.InvalidCalls++
					}
					if result.Schema.Attempts == 1 {
						result.Schema.FirstToolCorrect = a.Call.Name == f.Schema.Operation
						result.Schema.FirstArgumentsValid = valid
					}
				}
				continue
			}
			result.ToolCalls++
			before, exists := starts[a.InvocationID]
			if !exists {
				check(result, "trace.tool_start", "harness", false, "matching invocation start", a.InvocationID, x.record.Cursor(), a.InvocationID)
				continue
			}
			delete(starts, a.InvocationID)
			callChanges := changes[before.changeIndex:]
			batchRejected := mixedControl[a.InvocationID]
			// Scripted probes isolate one error. Live calls may combine several
			// mistakes, so their decoder can legitimately report another field.
			if before.probe && result.Mode == Scripted {
				probes++
				check(result, "schema.probe_invalid", "harness", !before.valid, "regression probe rejected by advertised schema", before.valid, x.record.Cursor(), a.InvocationID)
				check(result, "schema.probe_error", "harness", a.Err != nil && strings.Contains(a.Err.Error(), before.errorNeedle), before.errorNeedle, fmt.Sprint(a.Err), x.record.Cursor(), a.InvocationID)
			}
			if batchRejected {
				check(result, "batch.rejected", "harness", a.Err != nil && a.Err.Error() == schemaMixedControlError, schemaMixedControlError, fmt.Sprint(a.Err), x.record.Cursor(), a.InvocationID)
				check(result, "batch.rejection_atomic", "harness", len(callChanges) == 0 && schemaEqual(before.before, state), "no mutations before the argument decoder", callChanges, x.record.Cursor(), a.InvocationID)
				if a.Err != nil {
					result.Behavior.RejectedCalls++
				}
			} else if !before.valid {
				rejected := a.Err != nil && schemaRejection(a.Call.Name, before.known, a.Err.Error())
				check(result, "schema.rejected", "harness", rejected, "argument validation or exact unknown-tool rejection", fmt.Sprint(a.Err), x.record.Cursor(), a.InvocationID)
				check(result, "schema.rejection_atomic", "harness", len(callChanges) == 0 && schemaEqual(before.before, state), "no ledger change events or record changes", callChanges, x.record.Cursor(), a.InvocationID)
				if a.Err != nil {
					if before.known {
						result.Behavior.RejectedCalls++
					} else {
						result.Behavior.UnknownToolErrors++
					}
				}
			} else if a.Err != nil {
				check(result, "schema.decoder_agreement", "harness", !schemaRejection(a.Call.Name, true, a.Err.Error()), "schema-valid arguments reach semantic validation", a.Err.Error(), x.record.Cursor(), a.InvocationID)
				result.Behavior.UnknownToolErrors++
			}
			if before.late || (a.Err == nil && !readOnly(a.Call.Name) && !before.intended) {
				extraActions++
			}
			if a.Err == nil && before.intended {
				accepted++
				want, mutationOK, receiptOK := expectedSchemaTransition(f, before.before, a.Call, a.Result.Content.Text(), callChanges)
				check(result, "schema.transition", "harness", mutationOK && schemaEqual(want, state), "exact intended transition, record binding, and no collateral records", callChanges, x.record.Cursor(), a.InvocationID)
				check(result, "schema.receipt", "harness", receiptOK, "receipt agrees with committed transition", a.Result.Content.Text(), x.record.Cursor(), a.InvocationID)
				if accepted == 1 {
					expected = want
				}
			}
		case conversation.UsageEvent:
			if !observed || e.Agent != f.actor() {
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
		initial, expected = state.clone(), state.clone()
	}
	result.Behavior.OutputErrors = max(result.Behavior.OutputErrors, failedOutputs)
	if usageCount > 0 && inputKnown {
		result.InputTokens = &input
	}
	if usageCount > 0 && outputKnown {
		result.OutputTokens = &output
	}
	check(result, "outcome.one_operation", "behavior", accepted == 1, 1, accepted, result.Through, "")
	check(result, "outcome.exact_state", "behavior", accepted == 1 && schemaEqual(expected, state), "one requested transition and otherwise unchanged complete ledger", state, result.Through, "")
	check(result, "effects.no_extra_actions", "behavior", extraActions == 0, 0, extraActions, result.Through, "")
	check(result, "effects.no_extra_agents", "behavior", newAgents == 0, 0, newAgents, result.Through, "")
	check(result, "effects.no_extra_changes", "behavior", len(changes) == accepted, accepted, len(changes), result.Through, "")
	check(result, "records.preexisting_immutable", "harness", immutableSchemaRecords(initial, state), "all existing immutable records preserved", "compared every submission, audit, brief, and report", result.Through, "")
	if result.Mode == Scripted {
		check(result, "scenario.schema_probe_exercised", "harness", probes == 1, 1, probes, result.Through, "")
	}
	result.Behavior.Scorable = true
	updateScores(result)
}

const schemaMixedControlError = "control tool must be the sole call; no calls in this batch executed"

// Control-batch validation runs before argument decoding. Require the actual
// complete batch as evidence rather than trusting an arbitrary error string.
func schemaMixedControlInvocations(f fixture, facts []fact, hasControl bool) map[string]bool {
	out := map[string]bool{}
	if !hasControl {
		return out
	}
	var pending []agent.ToolActivity
	for _, x := range facts {
		switch e := x.event.(type) {
		case conversation.ToolEvent:
			if e.Agent == f.actor() && !e.Activity.FinishedAt.IsZero() {
				pending = append(pending, e.Activity)
			}
		case conversation.ToolBatchEvent:
			if e.Agent != f.actor() {
				continue
			}
			var ids []string
			control := false
			for _, call := range pending {
				ids = append(ids, call.Call.ID)
				control = control || call.Call.Name == "wait_for_input"
			}
			if control && len(pending) > 1 && slices.Equal(ids, e.Batch.Calls) {
				for _, call := range pending {
					out[call.InvocationID] = true
				}
			}
			pending = nil
		}
	}
	return out
}

func schemaRejection(name string, known bool, message string) bool {
	if !known {
		return message == "unknown tool: "+name
	}
	return strings.Contains(message, "arguments.") || strings.Contains(message, "arguments must ") || strings.Contains(message, "arguments requires ") || strings.Contains(message, "invalid JSON")
}

// This classifier is deliberately independent of production argument decoding.
// It identifies the historical bad shape, not the tool-call ID or script index.
func schemaProbe(id, name string, raw json.RawMessage) (bool, string) {
	var args map[string]json.RawMessage
	if json.Unmarshal(raw, &args) != nil {
		return false, ""
	}
	has := func(key string) bool { _, ok := args[key]; return ok }
	switch id {
	case "schema-audit-repair-field":
		return name == "assign_audit" && has("audit_id") && !has("submission_id"), "arguments.audit_id is not an allowed field"
	case "schema-audit-required":
		return name == "assign_audit" && has("assignee") && !has("work_id") && !has("expected_revision") && !has("submission_id"), "arguments.expected_revision is required"
	case "schema-audit-kind":
		return name == "assign_audit" && has("kind"), "arguments.kind is not an allowed field"
	case "schema-audit-null-extra":
		return name == "assign_audit" && has("context") && bytes.Equal(bytes.TrimSpace(args["context"]), []byte("null")), "arguments.context is not an allowed field"
	case "schema-removed-assignment-tool":
		return name == "assign_work", "unknown tool: assign_work"
	case "schema-audit-fail-findings":
		return name == "submit_audit" && string(args["verdict"]) == `"fail"` && !has("findings"), "arguments.findings is required"
	case "schema-audit-pass-findings":
		var findings []json.RawMessage
		_ = json.Unmarshal(args["findings"], &findings)
		return name == "submit_audit" && string(args["verdict"]) == `"pass"` && len(findings) > 0, "arguments.findings permits at most 0 items"
	case "schema-progress-objective":
		return name == "report_work_progress" && has("objective") && !has("position"), "arguments.objective is not an allowed field"
	}
	return false, ""
}

func schemaIntended(f fixture, call provider.ToolCall) bool {
	if call.Name != f.Schema.Operation {
		return false
	}
	switch call.Name {
	case "assign_audit":
		var a struct {
			Assignee   string            `json:"assignee"`
			WorkID     work.ID           `json:"work_id"`
			Revision   work.Revision     `json:"expected_revision"`
			Submission work.SubmissionID `json:"submission_id"`
		}
		return decodeSchemaArgs(call.Arguments, &a) == nil && a.Assignee == string(f.Auditor) && a.WorkID == f.Original.ID && a.Revision == f.Original.Revision && a.Submission == f.Submission.ID
	case "submit_audit":
		var a work.AuditRequest
		if decodeSchemaArgs(call.Arguments, &a) != nil || a.ID != f.Schema.Target.ID || a.ExpectedRevision != f.Schema.Target.Revision || a.SubmissionID != f.Submission.ID || a.Verdict != f.Schema.Verdict || strings.TrimSpace(a.Summary) == "" {
			return false
		}
		if a.Verdict == work.Pass {
			return len(a.Findings) == 0
		}
		if len(a.Findings) == 0 {
			return false
		}
		for _, finding := range a.Findings {
			if strings.TrimSpace(finding.Description) == "" || strings.TrimSpace(finding.RequiredChange) == "" || strings.TrimSpace(finding.Verification) == "" || len(finding.StepIDs) != 1 || finding.StepIDs[0] != f.Plan.Steps[0].ID {
				return false
			}
		}
		return true
	case "report_work_progress":
		var a work.ReportWorkProgressRequest
		return decodeSchemaArgs(call.Arguments, &a) == nil && a.WorkID == f.Schema.Target.ID && a.Position != nil && schemaEqual(a.Position, &f.Schema.ExpectedPosition) && len(a.Findings) == 0 && len(a.Steps) == 0
	}
	return false
}

// Generate the permitted state independently from fixture facts and submitted
// prose. Only generated IDs and report timestamps are learned from the event.
func expectedSchemaTransition(f fixture, before schemaLedger, call provider.ToolCall, receipt string, changes []work.Change) (schemaLedger, bool, bool) {
	want := before.clone()
	if len(changes) != 1 {
		return want, false, false
	}
	c := changes[0]
	original := before.Works[f.Original.ID].Clone()
	var expected work.Change
	receiptOK := false
	switch call.Name {
	case "assign_audit":
		var created work.Work
		for _, w := range c.Works {
			if _, exists := before.Works[w.ID]; !exists {
				created = w
			}
		}
		if created.ID == "" {
			return want, false, false
		}
		audit := work.Work{ID: created.ID, Kind: work.AuditWork, State: work.Active, Revision: 1, AssignedAtRevision: 1, Owner: f.Root, RequestedBy: f.actor(), Assignee: f.Auditor, ParentID: original.ID, SubjectSubmissionID: f.Submission.ID, Task: "Audit the submitted outcome: " + original.Task, ExpectedOutput: "Submit a pass or fail verdict with evidence. If unable to verify, report a blocker.", Scope: original.Scope}
		original.State, original.Revision = work.Checking, original.Revision+1
		expected.Works = []work.Work{original, audit}
		var got work.Work
		receiptOK = json.Unmarshal([]byte(receipt), &got) == nil && schemaEqual(got, audit)
	case "submit_audit":
		if len(c.Audits) != 1 || c.Audits[0].ID == "" {
			return want, false, false
		}
		if _, exists := before.Audits[c.Audits[0].ID]; exists {
			return want, false, false
		}
		var request work.AuditRequest
		if decodeSchemaArgs(call.Arguments, &request) != nil {
			return want, false, false
		}
		audit := work.Audit{ID: c.Audits[0].ID, WorkID: f.Schema.Target.ID, SubmissionID: f.Submission.ID, ReviewedBy: f.actor(), Verdict: f.Schema.Verdict, Summary: request.Summary, Findings: request.Findings}
		original.State = work.ChangesRequested
		stepState := work.Pending
		if f.Schema.Verdict == work.Pass {
			original.State, stepState = work.Accepted, work.Completed
		}
		original.Revision++
		original.LatestAuditID = audit.ID
		auditWork := before.Works[f.Schema.Target.ID].Clone()
		auditWork.State, auditWork.Revision = work.Closed, auditWork.Revision+1
		plan := before.Plans[f.Plan.ID].Clone()
		for i := range plan.Steps {
			if slices.Contains(original.Scope.StepIDs, plan.Steps[i].ID) {
				plan.Steps[i].Status = stepState
			}
		}
		expected.Works, expected.Plans, expected.Audits = []work.Work{original, auditWork}, []work.Plan{plan}, []work.Audit{audit}
		var got work.Audit
		receiptOK = json.Unmarshal([]byte(receipt), &got) == nil && schemaEqual(got, audit)
	case "report_work_progress":
		if len(c.ProgressReports) != 1 || c.ProgressReports[0].ID == "" || c.ProgressReports[0].RecordedAt.IsZero() {
			return want, false, false
		}
		if _, exists := before.Reports[c.ProgressReports[0].ID]; exists {
			return want, false, false
		}
		var request work.ReportWorkProgressRequest
		if decodeSchemaArgs(call.Arguments, &request) != nil {
			return want, false, false
		}
		report := work.WorkProgressReport{ID: c.ProgressReports[0].ID, WorkID: f.Schema.Target.ID, Author: f.actor(), AssignedAtRevision: f.Schema.Target.AssignedAtRevision, WorkRevision: f.Schema.Target.Revision + 1, RecordedAt: c.ProgressReports[0].RecordedAt, Position: request.Position}
		w := before.Works[f.Schema.Target.ID].Clone()
		w.Revision, w.LatestProgressReportID, w.LatestPositionReportID = report.WorkRevision, report.ID, report.ID
		w.Note, w.Blocker = request.Position.Note, request.Position.Blocker
		expected.Works, expected.ProgressReports = []work.Work{w}, []work.WorkProgressReport{report}
		wantReceipt := work.ReportWorkProgressResult{WorkID: w.ID, WorkRevision: w.Revision, AssignedAtRevision: w.AssignedAtRevision, ReportID: report.ID, RecordedAt: report.RecordedAt}
		var got work.ReportWorkProgressResult
		receiptOK = json.Unmarshal([]byte(receipt), &got) == nil && schemaEqual(got, wantReceipt)
	default:
		return want, false, false
	}
	want.apply(expected)
	return want, schemaEqual(c, expected), receiptOK
}

func immutableSchemaRecords(before, after schemaLedger) bool {
	for id, v := range before.Submissions {
		if !schemaEqual(v, after.Submissions[id]) {
			return false
		}
	}
	for id, v := range before.Audits {
		if !schemaEqual(v, after.Audits[id]) {
			return false
		}
	}
	for id, v := range before.Briefs {
		if !schemaEqual(v, after.Briefs[id]) {
			return false
		}
	}
	for id, v := range before.Reports {
		if !schemaEqual(v, after.Reports[id]) {
			return false
		}
	}
	return true
}

func addSchemaReplayAssertion(result *Result, f fixture, live, replay []fact) {
	project := func(facts []fact) (schemaLedger, []work.Event) {
		state := newSchemaLedger()
		var events []work.Event
		for _, x := range facts {
			if e, ok := x.event.(conversation.WorkEvent); ok {
				events = append(events, e.Event.Clone())
				if e.Event.Change != nil {
					state.apply(*e.Event.Change)
				}
			}
		}
		return state, events
	}
	before, beforeEvents := project(live)
	after, afterEvents := project(replay)
	check(result, "trace.replay_consistency", "harness", schemaEqual(before, after) && schemaEqual(beforeEvents, afterEvents), "identical complete ledger and domain event history at grading prefix", schemaEqual(before, after), result.Through, "")
	previous := result.Outcome
	updateScores(result)
	if result.ErrorClass == "budget" {
		result.Outcome = previous
		result.Behavior.OutcomeCorrect, result.Behavior.CleanSuccess, result.Behavior.RecoverySuccess = false, false, false
	}
}

// JSON omitempty intentionally erases empty optional slices in saved traces.
// Compare the serialized ledger contract rather than Go allocation details.
func schemaEqual(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	return err == nil && bytes.Equal(left, right)
}
