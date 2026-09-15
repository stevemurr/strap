package tool

import (
	"encoding/json"
	"github.com/stevemurr/strap/work"
)

var progressParameters = parameters[work.ReportWorkProgressRequest](
	MinLength("work_id", 1), Minimum("expected_revision", 1), Minimum("assigned_at_revision", 1),
	AtLeastOne("", "position", "findings", "steps"), MinLength("position.objective", 1), MaxItems("findings", 16),
	Enum("findings[].basis", "observed", "inferred", "retracted"), MinLength("findings[].claim", 1), MaxItems("findings[].evidence", 8), MinLength("findings[].evidence[].uri", 1),
	MinLength("steps[].step_id", 1), Enum("steps[].status", "pending", "in_progress", "blocked", "ready_for_review"),
)

func DecodeProgressReport(raw json.RawMessage) (work.ReportWorkProgressRequest, error) {
	return progressParameters.Decode(raw)
}
func ReportWorkProgress(h Handler[work.ReportWorkProgressRequest]) Tool {
	return Func[work.ReportWorkProgressRequest]{Spec: Definition[work.ReportWorkProgressRequest]{Bookkeeping: []string{"expected_revision", "assigned_at_revision"}, Name: "report_work_progress", Description: "Record progress for your active assignment using work_id, expected_revision and assigned_at_revision from current work. Supply a full position to replace its previous fields; omit position to preserve it. Findings accumulate; corrections/retractions reference supersedes. Only scoped implementation/repair work may update steps. The receipt returns work_revision for the next mutation. Reporting does not submit an outcome or complete work.", Parameters: progressParameters}, Invoke: h}
}
