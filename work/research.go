package work

import "github.com/stevemurr/strap/identity"

type ResearchAssignRequest struct {
	Assignee       identity.ActorID `json:"assignee"`
	Task           string           `json:"task"`
	Context        string           `json:"context,omitempty"`
	ExpectedOutput string           `json:"expected_output,omitempty"`
}

func (s *Store) AssignResearch(actor identity.ActorID, r ResearchAssignRequest) (result Work, err error) {
	if err = s.beginMutation(); err != nil {
		return result, err
	}
	defer s.endMutation(&err)
	if blank(string(actor)) || blank(string(r.Assignee)) || blank(r.Task) {
		return result, invalid("actor, assignee and task required")
	}
	w := Work{ID: ID(s.id("work")), Kind: Research, State: Active, Revision: 1, AssignedAtRevision: 1, Owner: actor, RequestedBy: actor, Assignee: r.Assignee, Task: r.Task, Context: r.Context, ExpectedOutput: r.ExpectedOutput}
	s.putWork(w.ID, w)
	s.emit(WorkAssigned, actor, w, true)
	return w.Clone(), nil
}
