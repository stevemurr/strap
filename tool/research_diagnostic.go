package tool

import (
	"github.com/stevemurr/strap/work"
	"time"
)

type ResearchDiagnosticArgs struct {
	WorkID             work.ID       `json:"work_id"`
	AssignedAtRevision work.Revision `json:"assigned_at_revision"`
	Command            string        `json:"command"`
	TimeoutMS          *int64        `json:"timeout_ms,omitempty"`
}

func ResearchDiagnostic(description string, maximum time.Duration, h Handler[ResearchDiagnosticArgs]) Tool {
	return builtin("shell", description+" Select the active research assignment with work_id and assigned_at_revision. Diagnostic use may execute project code and write generated files; these limits do not enforce read-only access.", h, MinLength("work_id", 1), Minimum("assigned_at_revision", 1), MinLength("command", 1), Minimum("timeout_ms", 1), Maximum("timeout_ms", maximum.Milliseconds()))
}
