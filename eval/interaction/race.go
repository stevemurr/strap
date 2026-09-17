package interaction

import (
	"context"
	"fmt"
	"sync"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

const revisionRaceReason = "Competing audit withdrawn before review."

// revisionRace changes actual domain state after the model has composed its
// first otherwise-valid assignment and before production tool dispatch. Neither
// model arguments nor tool receipts are rewritten. A provider response is not
// executed until Submit returns, so no scheduling sleep is needed.
type revisionRace struct {
	session *harness.Session
	reader  *inspection.Reader
	fixture fixture
	mu      sync.Mutex
	record  *RevisionRace
	err     error
}

func (r *revisionRace) inject(ctx context.Context, response provider.Response) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.record != nil || r.err != nil {
		return r.err
	}
	for _, call := range response.ToolCalls {
		if call.Name != "assign_audit" {
			continue
		}
		request, err := tool.DecodeAssignment(call.Name, call.Arguments)
		f := r.fixture
		if err != nil || request.Kind != work.AuditWork || request.WorkID != f.Original.ID || request.Assignee != f.Auditor || request.SubmissionID != f.Submission.ID {
			continue
		}
		current, err := r.session.GetWork(ctx, f.Root, request.WorkID)
		if err != nil {
			r.err = fmt.Errorf("read revision-race precondition: %w", err)
			return r.err
		}
		if current.State != work.NeedsCheck || current.Revision != request.ExpectedRevision {
			continue
		}
		before, err := r.reader.Head(ctx)
		if err != nil {
			r.err = fmt.Errorf("capture revision-race start: %w", err)
			return r.err
		}
		audit, err := r.session.AssignWork(ctx, f.Root, request)
		if err != nil {
			r.err = fmt.Errorf("assign competing audit: %w", err)
			return r.err
		}
		cancelled, err := r.session.CancelWork(ctx, f.Root, work.CancelRequest{
			WorkTarget: work.WorkTarget{ID: audit.ID, ExpectedRevision: audit.Revision}, Reason: revisionRaceReason,
		})
		if err != nil {
			r.err = fmt.Errorf("withdraw competing audit: %w", err)
			return r.err
		}
		after, err := r.session.GetWork(ctx, f.Root, request.WorkID)
		if err != nil {
			r.err = fmt.Errorf("read revision-race outcome: %w", err)
			return r.err
		}
		through, err := r.reader.Head(ctx)
		if err != nil {
			r.err = fmt.Errorf("capture revision-race outcome: %w", err)
			return r.err
		}
		r.record = &RevisionRace{Before: before.Cursor, Through: through.Cursor, OriginalBefore: current, OriginalAfter: after, CancelledAudit: cancelled, TriggerCallID: call.ID, TriggerRequest: request}
		return nil
	}
	return nil
}

func (r *revisionRace) snapshot() (*RevisionRace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.record, r.err
}
