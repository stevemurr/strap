// Package interaction evaluates bounded agent decisions against real harness
// state transitions. Scripted conformance and live behavior share the same oracle.
package interaction

import (
	"time"

	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/work"
)

type Mode string

const (
	Scripted Mode = "scripted"
	Live     Mode = "live"
)

type Scenario struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Version int    `json:"version"`
}

// Version 2 uses operation-specific assignment tools without a kind field.
// Version 1 trial artifacts retain their recorded schemas and are historical.
func List() []Scenario {
	return append([]Scenario{
		{"audit-independent", "Assign an independent audit of the latest submission", 2},
		{"audit-wrong-assignee", "Reject an implementor as auditor, then assign correctly", 2},
		{"audit-old-submission", "Reject a superseded submission, then audit the latest", 2},
		{"audit-stale-revision", "Reject a stale revision, then use current work", 2},
		{"audit-revision-race", "Recover when a competing audit changes the revision before dispatch", 2},
	}, schemaScenarios()...)
}

type Options struct {
	Config       harness.Config
	Mode         Mode
	Output       string
	ScenarioIDs  []string
	Repetitions  int
	MaxCalls     int
	MaxToolCalls int
	Timeout      time.Duration
	Commit       string
	Profile      string
	// Provider is an optional borrowed live provider for integrations and tests.
	Provider provider.Provider
	// Observe receives completed trial results, sequentially.
	Observe func(Result)
}

type Assertion struct {
	ID         string          `json:"id"`
	Track      string          `json:"track"`
	Passed     bool            `json:"passed"`
	Expected   any             `json:"expected"`
	Actual     any             `json:"actual"`
	Cursor     eventlog.Cursor `json:"cursor"`
	Invocation string          `json:"invocation,omitempty"`
}

type Score struct {
	Passed int `json:"passed"`
	Total  int `json:"total"`
}

type Behavior struct {
	Scorable          bool `json:"scorable"`
	OutcomeCorrect    bool `json:"outcome_correct"`
	CleanSuccess      bool `json:"clean_success"`
	RecoverySuccess   bool `json:"recovery_success"`
	RejectedCalls     int  `json:"rejected_calls"`
	UnknownToolErrors int  `json:"unknown_tool_errors"`
	OutputErrors      int  `json:"output_errors"`
}

func (b *Behavior) failOutcome() {
	b.OutcomeCorrect, b.CleanSuccess, b.RecoverySuccess = false, false, false
}

// SchemaBehavior distinguishes first-operation selection and argument validity
// from eventual recovery. Reads before an operation do not consume an attempt.
type SchemaBehavior struct {
	Attempts            int  `json:"attempts"`
	InvalidCalls        int  `json:"invalid_calls"`
	FirstToolCorrect    bool `json:"first_tool_correct"`
	FirstArgumentsValid bool `json:"first_arguments_valid"`
}

type Result struct {
	ScenarioID   string          `json:"scenario_id"`
	Trial        int             `json:"trial"`
	Mode         Mode            `json:"mode"`
	Outcome      string          `json:"outcome"`
	Error        string          `json:"error,omitempty"`
	ErrorClass   string          `json:"error_class,omitempty"`
	StartedAt    time.Time       `json:"started_at"`
	Duration     time.Duration   `json:"duration_ns"`
	Trace        string          `json:"trace"`
	Manifest     string          `json:"manifest"`
	Start        eventlog.Cursor `json:"start"`
	Through      eventlog.Cursor `json:"through"`
	StopReason   string          `json:"stop_reason"`
	ModelCalls   int             `json:"model_calls"`
	ToolCalls    int             `json:"tool_calls"`
	InputTokens  *int64          `json:"input_tokens,omitempty"`
	OutputTokens *int64          `json:"output_tokens,omitempty"`
	Harness      Score           `json:"harness"`
	Behavior     Behavior        `json:"behavior"`
	Schema       *SchemaBehavior `json:"schema,omitempty"`
	Assertions   []Assertion     `json:"assertions"`
	RevisionRace *RevisionRace   `json:"revision_race,omitempty"`
}

// RevisionRace identifies the host intervention independently of actor calls.
// The trace prefix (Before, Through] must contain exactly the declared public
// assignment/cancellation effects. The oracle validates it before allowing them.
type RevisionRace struct {
	Before         eventlog.Cursor        `json:"before"`
	Through        eventlog.Cursor        `json:"through"`
	OriginalBefore work.Work              `json:"original_before"`
	OriginalAfter  work.Work              `json:"original_after"`
	CancelledAudit work.Work              `json:"cancelled_audit"`
	TriggerCallID  string                 `json:"trigger_call_id"`
	TriggerRequest work.AssignmentRequest `json:"trigger_request"`
}

type Report struct {
	Version       int      `json:"version"`
	Mode          Mode     `json:"mode"`
	PlannedTrials int      `json:"planned_trials,omitempty"`
	Results       []Result `json:"results"`
}
