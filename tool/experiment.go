package tool

import (
	"context"
	"time"

	"github.com/stevemurr/strap/work"
)

func RecordHypothesis(h Handler[work.RecordHypothesisRequest]) Tool {
	return builtin("record_hypothesis", "Record a hypothesis on your active experiment before you measure it: statement is the claim, prediction is the observation that would confirm or refute it, method is how you will measure it. Only runs you make after this call can be cited for it. Returns its hypothesis_id.", h,
		MinLength("work_id", 1), MinLength("statement", 1), MinLength("prediction", 1), MinLength("method", 1))
}

type recordResultArgs struct {
	WorkID       work.ID                `json:"work_id"`
	HypothesisID work.HypothesisID      `json:"hypothesis_id"`
	Verdict      work.HypothesisVerdict `json:"verdict"`
	Observed     string                 `json:"observed"`
	EvidenceRefs []string               `json:"evidence_refs"`
}

func RecordResult(h Handler[work.RecordResultRequest]) Tool {
	return builtin("record_result", "Record a hypothesis's result, once: supported or refuted against its prediction, or inconclusive. observed states what the measurement showed, with its numbers. evidence_refs are the evidence_ref values your shell or run_trials results returned for runs made after the hypothesis; supported and refuted need at least one.", func(ctx context.Context, c Call, a recordResultArgs) (Result, error) {
		return h(ctx, c, work.RecordResultRequest{WorkID: a.WorkID, HypothesisID: a.HypothesisID, Verdict: a.Verdict, Observed: a.Observed, EvidenceRefs: a.EvidenceRefs})
	}, Nullable("evidence_refs", "no runs; only for an inconclusive result"), MinLength("work_id", 1), MinLength("hypothesis_id", 1), Enum("verdict", "supported", "refuted", "inconclusive"), MinLength("observed", 1), MaxItems("evidence_refs", 8))
}

// SubmitExperimentInput names the method's files by path; the host captures
// their contents from the experimenter's copy of the workspace.
type SubmitExperimentInput struct {
	work.WorkTarget
	Summary        string              `json:"summary"`
	Method         MethodInput         `json:"method"`
	Recommendation *string             `json:"recommendation"`
	ProposedSteps  []ProposedStepInput `json:"proposed_steps"`
}
type MethodInput struct {
	ReproduceCommand string   `json:"reproduce_command"`
	Files            []string `json:"files"`
}

// Request returns the ledger request with the method files' contents.
func (a SubmitExperimentInput) Request(files []work.MethodFile) work.SubmitExperimentRequest {
	return work.SubmitExperimentRequest{WorkTarget: a.WorkTarget, Summary: a.Summary, Method: work.ExperimentMethod{ReproduceCommand: a.Method.ReproduceCommand, Files: files}, Recommendation: valueOrZero(a.Recommendation), ProposedSteps: mapInputs(a.ProposedSteps, ProposedStepInput.domain)}
}

var submitExperimentDefinition = Definition[SubmitExperimentInput]{Name: "submit_experiment", Description: "Deliver your experiment's conclusion once every hypothesis has a result; a refuted or inconclusive result is a full result, and finding nothing wrong is a conclusion. Use the current work revision as expected_revision. method.reproduce_command reruns the measurement; method.files are the paths, in your copy of the workspace, of the measurement harness you wrote, captured into the conclusion so others can add it or rerun it. Proposed steps do not change the plan.", Parameters: parameters[SubmitExperimentInput](Nullable("method.files", "no harness files"), Nullable("recommendation", "no value in the replacement or new record"), Nullable("proposed_steps", "no value in the replacement or new record"), Nullable("proposed_steps[].acceptance_criteria", "no value in the replacement or new record"), MinLength("work_id", 1), Minimum("expected_revision", 1), MinLength("summary", 1), MinLength("method.reproduce_command", 1), MaxItems("method.files", 16), MaxItems("proposed_steps", 32)), Bookkeeping: []string{"expected_revision"}}

func SubmitExperiment(h Handler[SubmitExperimentInput]) Tool {
	return Func[SubmitExperimentInput]{Spec: submitExperimentDefinition, Invoke: h}
}

type getConclusionArgs struct {
	ID work.ConclusionID `json:"conclusion_id"`
}

func GetConclusion(h Handler[work.ConclusionID]) Tool {
	return builtin("get_conclusion", "Read a delivered experiment conclusion by conclusion_id: its hypotheses with their results and evidence refs, the method that reproduces the measurement, including the harness files, and any recommendation and proposed steps.", func(ctx context.Context, c Call, a getConclusionArgs) (Result, error) {
		return h(ctx, c, a.ID)
	}, MinLength("conclusion_id", 1))
}

// RunTrialsArgs repeats one command to measure it.
type RunTrialsArgs struct {
	Command   string `json:"command"`
	Trials    int    `json:"trials"`
	TimeoutMS *int64 `json:"timeout_ms"`
}

// Trial is one run of a run_trials command.
type Trial struct {
	ElapsedMS float64 `json:"elapsed_ms"`
	ExitCode  *int    `json:"exit_code"`
	TimedOut  bool    `json:"timed_out,omitempty"`
}

// TrialsResult is what run_trials returns: every trial, their wall-clock
// spread, and the last run's output.
type TrialsResult struct {
	Trials     []Trial `json:"trials"`
	MedianMS   float64 `json:"median_ms"`
	MinMS      float64 `json:"min_ms"`
	MaxMS      float64 `json:"max_ms"`
	Failed     int     `json:"failed"`
	LastOutput string  `json:"last_output"`
}

// MaxTrials bounds one run_trials call.
const MaxTrials = 20

func RunTrials(maximum time.Duration, h Handler[RunTrialsArgs]) Tool {
	return builtin("run_trials", "Run one shell command several times in your copy of the workspace and measure it: returns each trial's wall-clock time and exit code, the median, minimum and maximum, and the last trial's output. One timing proves little; use several trials. timeout_ms bounds each trial. The result carries one evidence_ref for all trials.", h,
		Nullable("timeout_ms", "use the configured timeout"), MinLength("command", 1), Minimum("trials", 1), Maximum("trials", MaxTrials), Minimum("timeout_ms", 1), Maximum("timeout_ms", maximum.Milliseconds()))
}
