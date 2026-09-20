package tool

import (
	"github.com/stevemurr/strap/work"
	"time"
)

type ResearchDiagnosticArgs struct {
	WorkID    work.ID `json:"work_id"`
	Command   string  `json:"command"`
	TimeoutMS *int64  `json:"timeout_ms"`
}

func ResearchDiagnostic(description string, maximum time.Duration, h Handler[ResearchDiagnosticArgs]) Tool {
	return builtin("shell", description+" Select the active research assignment with work_id. Diagnostic use may execute project code and write generated files; these limits do not enforce read-only access.", h, Nullable("timeout_ms", "use the configured timeout"), MinLength("work_id", 1), MinLength("command", 1), Minimum("timeout_ms", 1), Maximum("timeout_ms", maximum.Milliseconds()))
}
