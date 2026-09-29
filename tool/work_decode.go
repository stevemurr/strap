package tool

import (
	"encoding/json"
	"errors"
	"github.com/stevemurr/strap/work"
)

var submitWorkDefinition = Definition[SubmitInput]{Name: "submit_work", Description: "Submit implementation or repairs for audit. All scoped steps must be ready_for_review and your blocker cleared. The receipt's work_revision is current for any later mutation. A text reply does not submit work.", Parameters: parameters[SubmitInput](Nullable("evidence", "no value in the replacement or new record"), Nullable("artifacts", "no value in the replacement or new record"), Nullable("artifacts[].revision", "no value in the replacement or new record"), Minimum("expected_revision", 1), MinLength("work_id", 1), MinLength("summary", 1), MinLength("artifacts[].uri", 1)), Bookkeeping: []string{"expected_revision"}}
var passAuditDefinition = Definition[AuditInput]{Name: "pass_audit", Description: "", Parameters: parameters[AuditInput](Minimum("expected_revision", 1), MinLength("work_id", 1), MinLength("submission_id", 1), Enum("verdict", "pass"), Nullable("findings", "no value in the replacement or new record"), Nullable("findings[].step_ids", "no value in the replacement or new record"), MinLength("summary", 1), MaxItems("findings", 0)), Bookkeeping: []string{"expected_revision"}}
var failAuditDefinition = Definition[AuditInput]{Name: "fail_audit", Description: "", Parameters: parameters[AuditInput](Minimum("expected_revision", 1), MinLength("work_id", 1), MinLength("submission_id", 1), Enum("verdict", "fail"), Nullable("findings[].step_ids", "no value in the replacement or new record"), MinLength("summary", 1), MinItems("findings", 1), MinLength("findings[].description", 1), MinLength("findings[].required_change", 1), MinLength("findings[].verification", 1)), Bookkeeping: []string{"expected_revision"}}
var cancelWorkDefinition = Definition[work.CancelRequest]{Name: "cancel_work", Description: "Owner cancels work. Cancelling implementation or repair ends that cycle; cancelling audit returns its submission for another review.", Parameters: parameters[work.CancelRequest](MinLength("work_id", 1), Minimum("expected_revision", 1), MinLength("reason", 1)), Bookkeeping: []string{"expected_revision"}}
var submitBriefDefinition = Definition[SubmitBriefInput]{Name: "submit_brief", Description: "Deliver an immutable brief of your review or research. Use the current work revision as expected_revision. Record new findings here with findings, or cite current finding IDs from report_work_progress receipts with finding_ids; the brief cites both. Proposed steps do not change the plan. Delivery ends this investigation and does not accept implementation work.", Parameters: parameters[SubmitBriefInput](Nullable("finding_ids", "no value in the replacement or new record"), Nullable("findings", "record no new findings"), Nullable("findings[].evidence", "no value in the replacement or new record"), Nullable("findings[].evidence[].revision", "no value in the replacement or new record"), Nullable("findings[].evidence[].locator", "no value in the replacement or new record"), Nullable("findings[].evidence[].detail", "no value in the replacement or new record"), Nullable("findings[].limitation", "no value in the replacement or new record"), Nullable("findings[].supersedes", "no value in the replacement or new record"), MaxItems("findings", 16), Enum("findings[].basis", "observed", "inferred", "retracted"), MinLength("findings[].claim", 1), MaxItems("findings[].evidence", 8), MinLength("findings[].evidence[].uri", 1), Nullable("open_questions", "no value in the replacement or new record"), Nullable("recommendation", "no value in the replacement or new record"), Nullable("proposed_steps", "no value in the replacement or new record"), Nullable("proposed_steps[].acceptance_criteria", "no value in the replacement or new record"), MinLength("work_id", 1), Minimum("expected_revision", 1), MinLength("summary", 1), MaxItems("finding_ids", 256), MaxItems("open_questions", 32), MaxItems("proposed_steps", 32), Reject("", "assigned_at_revision", "submit_research takes no assignment binding; the brief records it from current work")), Bookkeeping: []string{"expected_revision"}}

func DecodeCancellation(raw json.RawMessage) (work.CancelRequest, error) {
	return cancelWorkDefinition.Parameters.Decode(raw)
}
func DecodeSubmission(raw json.RawMessage) (work.SubmitRequest, error) {
	a, e := submitWorkDefinition.Parameters.Decode(raw)
	return a.domain(), e
}
func DecodeBriefSubmission(raw json.RawMessage) (work.SubmitBriefRequest, error) {
	a, e := submitBriefDefinition.Parameters.Decode(raw)
	return a.domain(), e
}
func DecodeAuditSubmission(raw json.RawMessage) (work.AuditRequest, error) {
	var failures []error
	for _, d := range []Definition[AuditInput]{passAuditDefinition, failAuditDefinition} {
		a, e := d.Parameters.Decode(raw)
		if e == nil {
			return a.domain(), nil
		}
		failures = append(failures, e)
	}
	return work.AuditRequest{}, errors.Join(failures...)
}
