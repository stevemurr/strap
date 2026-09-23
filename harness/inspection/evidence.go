package inspection

import (
	"context"
	"encoding/json"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/work"
)

type ExecutionEvidence struct {
	Record    eventlog.Cursor        `json:"record"`
	Execution conversation.ToolEvent `json:"execution"`
}

func (v *View) GetExecutionEvidence(ctx context.Context, actor identity.ActorID, ref string) (ExecutionEvidence, error) {
	id, ok := v.indexes.evidence[ref]
	if !ok {
		return ExecutionEvidence{}, work.ErrNotFound
	}
	t := v.indexes.tools[id]
	if t.Execution == nil || t.FinishRecord == nil {
		return ExecutionEvidence{}, work.ErrNotFound
	}
	model, _, err := v.workModel(ctx)
	if err != nil {
		return ExecutionEvidence{}, err
	}
	if err := model.CanReadExecution(actor, work.ID(t.Execution.WorkID)); err != nil {
		return ExecutionEvidence{}, err
	}
	e, err := v.ResolveRecord(ctx, eventlog.Record{Session: v.id, Sequence: t.FinishRecord.Sequence})
	if err != nil {
		return ExecutionEvidence{}, err
	}
	decoded, err := eventcodec.DecodeEvent(e)
	if err != nil {
		return ExecutionEvidence{}, err
	}
	execution, ok := decoded.(conversation.ToolEvent)
	if !ok || execution.Activity.Result.Execution == nil || execution.Activity.Result.Execution.EvidenceRef != ref || execution.Activity.FinishedAt.IsZero() {
		return ExecutionEvidence{}, work.ErrInvalid
	}
	return ExecutionEvidence{Record: *t.FinishRecord, Execution: execution}, nil
}
func ReadExecutionEvidence(ctx context.Context, v *View, actor identity.ActorID, ref string) (work.ExecutionEvidence, json.RawMessage, error) {
	e, err := v.GetExecutionEvidence(ctx, actor, ref)
	if err != nil {
		return work.ExecutionEvidence{}, nil, err
	}
	// Encode through the canonical codec: Go error interfaces are not JSON errors.
	raw, err := json.Marshal(struct {
		Record     eventlog.Cursor  `json:"record"`
		Invocation string           `json:"invocation_id"`
		Actor      identity.ActorID `json:"actor"`
		Command    json.RawMessage  `json:"command"`
		Result     any              `json:"result"`
		StartedAt  any              `json:"started_at"`
		FinishedAt any              `json:"finished_at"`
		Error      string           `json:"error,omitempty"`
	}{e.Record, e.Execution.Activity.InvocationID, e.Execution.Agent, e.Execution.Activity.Call.Arguments, e.Execution.Activity.Result, e.Execution.Activity.StartedAt, e.Execution.Activity.FinishedAt, errorText(e.Execution.Activity.Err)})
	b := e.Execution.Activity.Result.Execution
	return work.ExecutionEvidence{WorkID: work.ID(b.WorkID), AssignedAtRevision: work.Revision(b.AssignedAtRevision), Actor: b.Actor}, raw, err
}
func errorText(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}
