package tool

import (
	"context"
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
	return lenientProgress{Func[work.ReportWorkProgressRequest]{Spec: Definition[work.ReportWorkProgressRequest]{Bookkeeping: []string{"expected_revision", "assigned_at_revision"}, Name: "report_work_progress", Description: "Record progress for your active assignment using work_id, expected_revision and assigned_at_revision from current work. Supply a full position to replace its previous fields; omit position to preserve it. Findings accumulate; corrections/retractions reference supersedes. Only scoped implementation/repair work may update steps. The receipt returns work_revision for the next mutation. Reporting does not submit an outcome or complete work.", Parameters: progressParameters}, Invoke: h}}
}

// lenientProgress repairs a slip every evaluated model made before the strict
// decoder sees the arguments: an objective at the top level instead of under
// position. Everything else is validated exactly as before.
type lenientProgress struct {
	Func[work.ReportWorkProgressRequest]
}

func (t lenientProgress) Call(ctx context.Context, c Call) (Result, error) {
	c.Arguments = normalizeProgressArguments(c.Arguments)
	return t.Func.Call(ctx, c)
}

func normalizeProgressArguments(raw json.RawMessage) json.RawMessage {
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return raw
	}
	if objective, ok := object["objective"]; ok {
		position, _ := object["position"].(map[string]any)
		if position == nil {
			position = map[string]any{}
		}
		if _, set := position["objective"]; !set {
			position["objective"] = objective
		}
		object["position"] = position
		delete(object, "objective")
	}
	out, err := json.Marshal(object)
	if err != nil {
		return raw
	}
	return out
}
