package tool

import (
	"encoding/json"

	"github.com/stevemurr/strap/work"
)

var progressParameters = parameters[work.ReportWorkProgressRequest](
	MinLength("work_id", 1),
	Reject("", "expected_revision", "report_work_progress takes no revision; a report adds to your own record and cannot collide with one"),
	Reject("", "assigned_at_revision", "report_work_progress takes no assignment binding; the report records it from current work"),
	// Recorded rejections cluster on three shapes: the position flattened onto
	// the request, a whole finding flattened onto it, and evidence abbreviated
	// or collapsed into the finding. Each hint names the field the author meant.
	Reject("", "objective", "objective belongs to position: position: {objective: ...}"),
	Reject("", "claim", "a finding belongs in findings: findings: [{claim, basis, evidence}]"),
	Reject("", "basis", "a finding belongs in findings: findings: [{claim, basis, evidence}]"),
	Reject("", "evidence", "evidence belongs to a finding: findings: [{claim, basis, evidence: [{uri}]}]"),
	Reject("findings[]", "e", "the field is evidence, an array of objects: evidence: [{uri}]"),
	Reject("findings[]", "uri", "evidence is an array of objects on the finding: evidence: [{uri}]"),
	Reject("findings[]", "limit", "the field is limitation, and an inferred finding requires it"),
	AtLeastOne("", "position", "findings", "steps"), MinLength("position.objective", 1), MaxItems("findings", 16),
	Enum("findings[].basis", "observed", "inferred", "retracted"), MinLength("findings[].claim", 1), MaxItems("findings[].evidence", 8), MinLength("findings[].evidence[].uri", 1),
	MinLength("steps[].step_id", 1), Enum("steps[].status", "pending", "in_progress", "blocked", "ready_for_review"),
)

func DecodeProgressReport(raw json.RawMessage) (work.ReportWorkProgressRequest, error) {
	return progressParameters.Decode(raw)
}
func ReportWorkProgress(h Handler[work.ReportWorkProgressRequest]) Tool {
	return Func[work.ReportWorkProgressRequest]{Spec: Definition[work.ReportWorkProgressRequest]{Name: "report_work_progress", Description: "Record progress for your active assignment using its work_id alone. Supply a full position to replace its previous fields; omit position to preserve it. Findings accumulate; corrections/retractions reference supersedes. Only scoped implementation/repair work may update steps. The receipt returns work_revision for the next mutation. Reporting does not submit an outcome or complete work.", Parameters: progressParameters}, Invoke: h}
}
