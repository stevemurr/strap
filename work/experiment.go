package work

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/stevemurr/strap/identity"
)

// An experiment asks whether something exists and how large it is: the
// experimenter records hypotheses before measuring, measures in its own copy
// of the workspace, records each hypothesis's result from runs it made after
// recording it, and delivers a conclusion. The ledger enforces that order, so
// what counts as confirmation is fixed before the data is seen.
const (
	Experiment          Kind      = "experiment"
	HypothesisRecorded  EventKind = "hypothesis_recorded"
	HypothesisResolved  EventKind = "hypothesis_resolved"
	ExperimentDelivered EventKind = "experiment_delivered"
)

type HypothesisID string
type ConclusionID string
type HypothesisVerdict string

const (
	Supported    HypothesisVerdict = "supported"
	Refuted      HypothesisVerdict = "refuted"
	Inconclusive HypothesisVerdict = "inconclusive"
)

// maxHypotheses bounds one experiment; hypotheses travel on its work item.
const maxHypotheses = 16

type Hypothesis struct {
	ID         HypothesisID      `json:"hypothesis_id"`
	RecordedAt time.Time         `json:"recorded_at"`
	Statement  string            `json:"statement"`
	Prediction string            `json:"prediction"` // The observation that would confirm or refute it.
	Method     string            `json:"method"`     // How it will be measured.
	Result     *HypothesisResult `json:"result,omitempty"`
}
type HypothesisResult struct {
	Verdict      HypothesisVerdict `json:"verdict"`
	Observed     string            `json:"observed"`
	EvidenceRefs []string          `json:"evidence_refs,omitempty"`
	RecordedAt   time.Time         `json:"recorded_at"`
}

func (h Hypothesis) Clone() Hypothesis {
	if h.Result != nil {
		r := *h.Result
		r.EvidenceRefs = slices.Clone(r.EvidenceRefs)
		h.Result = &r
	}
	return h
}

type ExperimentAssignRequest struct {
	Assignee       identity.ActorID `json:"assignee"`
	Task           string           `json:"task"`
	Context        string           `json:"context,omitempty"`
	ExpectedOutput string           `json:"expected_output,omitempty"`
	Scope          *Scope           `json:"scope,omitempty"` // Plan steps the experiment fulfils; delivery completes them.
}
type RecordHypothesisRequest struct {
	WorkID     ID     `json:"work_id"`
	Statement  string `json:"statement"`
	Prediction string `json:"prediction"`
	Method     string `json:"method"`
}
type RecordResultRequest struct {
	WorkID       ID                `json:"work_id"`
	HypothesisID HypothesisID      `json:"hypothesis_id"`
	Verdict      HypothesisVerdict `json:"verdict"`
	Observed     string            `json:"observed"`
	EvidenceRefs []string          `json:"evidence_refs,omitempty"`
}
type HypothesisReceipt struct {
	WorkID       ID             `json:"work_id"`
	WorkRevision Revision       `json:"work_revision"`
	Hypothesis   Hypothesis     `json:"hypothesis"`
	Unresolved   []HypothesisID `json:"unresolved,omitempty"` // Hypotheses still without a result.
}

// MethodFile is one file of the measurement harness, captured from the
// experimenter's copy when it delivers.
type MethodFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type ExperimentMethod struct {
	ReproduceCommand string       `json:"reproduce_command"`
	Files            []MethodFile `json:"files,omitempty"`
}
type SubmitExperimentRequest struct {
	WorkTarget
	Summary        string           `json:"summary"`
	Method         ExperimentMethod `json:"method"`
	Recommendation string           `json:"recommendation,omitempty"`
	ProposedSteps  []ProposedStep   `json:"proposed_steps,omitempty"`
}

// Conclusion is an experiment's immutable delivered result: its hypotheses
// with their results, and the method that reproduces the measurements.
type Conclusion struct {
	ID                 ConclusionID     `json:"conclusion_id"`
	Author             identity.ActorID `json:"author"`
	RecordedAt         time.Time        `json:"recorded_at"`
	WorkID             ID               `json:"work_id"`
	WorkRevision       Revision         `json:"work_revision"`
	AssignedAtRevision Revision         `json:"assigned_at_revision"`
	Summary            string           `json:"summary"`
	Hypotheses         []Hypothesis     `json:"hypotheses"`
	Method             ExperimentMethod `json:"method"`
	Recommendation     string           `json:"recommendation,omitempty"`
	ProposedSteps      []ProposedStep   `json:"proposed_steps,omitempty"`
}

func (c Conclusion) Clone() Conclusion {
	c.Hypotheses = cloneHypotheses(c.Hypotheses)
	c.Method.Files = slices.Clone(c.Method.Files)
	c.ProposedSteps = slices.Clone(c.ProposedSteps)
	for i := range c.ProposedSteps {
		c.ProposedSteps[i].AcceptanceCriteria = slices.Clone(c.ProposedSteps[i].AcceptanceCriteria)
	}
	return c
}

type SubmitExperimentResult struct {
	WorkID       ID           `json:"work_id"`
	WorkRevision Revision     `json:"work_revision"`
	State        State        `json:"state"`
	ConclusionID ConclusionID `json:"conclusion_id"`
	RecordedAt   time.Time    `json:"recorded_at"`
}

func cloneHypotheses(hs []Hypothesis) []Hypothesis {
	if hs == nil {
		return nil
	}
	out := make([]Hypothesis, len(hs))
	for i, h := range hs {
		out[i] = h.Clone()
	}
	return out
}

func (s *Store) AssignExperiment(actor identity.ActorID, r ExperimentAssignRequest) (result Work, err error) {
	if err = s.beginMutation(); err != nil {
		return result, err
	}
	defer s.endMutation(&err)
	if blank(string(actor)) || blank(string(r.Assignee)) || blank(r.Task) {
		return result, invalid("actor, assignee and task required")
	}
	if err = s.checkScope(actor, r.Scope); err != nil {
		return result, err
	}
	w := Work{ID: ID(s.id("work")), Kind: Experiment, State: Active, Revision: 1, AssignedAtRevision: 1, Owner: actor, RequestedBy: actor, Assignee: r.Assignee, Task: r.Task, Context: r.Context, ExpectedOutput: r.ExpectedOutput, Scope: r.Scope}.Clone()
	s.reserve(w)
	s.putWork(w.ID, w)
	s.emit(WorkAssigned, actor, w, true)
	return w.Clone(), nil
}

// activeExperiment returns the actor's active experiment id.
func (s *Store) activeExperiment(actor identity.ActorID, id ID) (Work, error) {
	w, err := s.assigned(actor, id, false)
	if err != nil {
		return Work{}, err
	}
	if w.Kind != Experiment {
		return Work{}, fmt.Errorf("%w: %s is %s work; hypotheses belong to an experiment", ErrState, w.ID, w.Kind)
	}
	if w.State != Active {
		return Work{}, fmt.Errorf("%w: experiment %s is %s", ErrState, w.ID, w.State)
	}
	return w, nil
}

// RecordHypothesis pre-registers a hypothesis on the actor's active
// experiment. Runs issued before it cannot be cited as its evidence.
func (s *Store) RecordHypothesis(actor identity.ActorID, r RecordHypothesisRequest) (result HypothesisReceipt, err error) {
	if err = s.beginMutation(); err != nil {
		return result, err
	}
	defer s.endMutation(&err)
	w, err := s.activeExperiment(actor, r.WorkID)
	if err != nil {
		return result, err
	}
	if blank(r.Statement) || blank(r.Prediction) || blank(r.Method) {
		return result, invalid("statement, prediction and method are required: say what you claim, what observation would confirm or refute it, and how you will measure it")
	}
	if err = prose(r.Statement, r.Prediction, r.Method); err != nil {
		return result, err
	}
	if len(w.Hypotheses) >= maxHypotheses {
		return result, invalid(fmt.Sprintf("an experiment holds at most %d hypotheses", maxHypotheses))
	}
	h := Hypothesis{ID: HypothesisID(s.id("hypothesis")), RecordedAt: time.Now().UTC(), Statement: r.Statement, Prediction: r.Prediction, Method: r.Method}
	s.order(string(h.ID))
	w.Hypotheses = append(w.Hypotheses, h)
	w.Revision++
	s.putWork(w.ID, w)
	s.emit(HypothesisRecorded, actor, w, false)
	return HypothesisReceipt{WorkID: w.ID, WorkRevision: w.Revision, Hypothesis: h.Clone(), Unresolved: unresolved(w)}, nil
}

// RecordResult records a hypothesis's verdict, once. Supported and refuted
// verdicts cite runs bound to this assignment that the experimenter started
// after recording the hypothesis.
func (s *Store) RecordResult(actor identity.ActorID, r RecordResultRequest) (result HypothesisReceipt, err error) {
	if err = s.beginMutation(); err != nil {
		return result, err
	}
	defer s.endMutation(&err)
	w, err := s.activeExperiment(actor, r.WorkID)
	if err != nil {
		return result, err
	}
	i := slices.IndexFunc(w.Hypotheses, func(h Hypothesis) bool { return h.ID == r.HypothesisID })
	if i < 0 {
		ids := make([]string, len(w.Hypotheses))
		for j, h := range w.Hypotheses {
			ids[j] = string(h.ID)
		}
		return result, fmt.Errorf("%w: hypothesis %s is not recorded on %s; %s", ErrNotFound, r.HypothesisID, w.ID, known("its hypotheses", "it has none: record one with record_hypothesis first", ids))
	}
	h := w.Hypotheses[i]
	if h.Result != nil {
		return result, fmt.Errorf("%w: %s already has a %s result; a result is recorded once", ErrState, h.ID, h.Result.Verdict)
	}
	switch r.Verdict {
	case Supported, Refuted:
		if len(r.EvidenceRefs) == 0 {
			return result, invalid("a supported or refuted verdict cites the evidence_ref of at least one run; use inconclusive when you have none")
		}
	case Inconclusive:
	default:
		return result, invalid("verdict must be supported, refuted or inconclusive")
	}
	if blank(r.Observed) {
		return result, invalid("observed is required: state what the measurement showed, with its numbers")
	}
	if err = prose(r.Observed); err != nil {
		return result, err
	}
	if len(r.EvidenceRefs) > 8 {
		return result, invalid("at most 8 evidence refs per result")
	}
	for _, ref := range r.EvidenceRefs {
		if err = s.checkRun(actor, w, h, ref); err != nil {
			return result, err
		}
	}
	h.Result = &HypothesisResult{Verdict: r.Verdict, Observed: r.Observed, EvidenceRefs: slices.Clone(r.EvidenceRefs), RecordedAt: time.Now().UTC()}
	w.Hypotheses = cloneHypotheses(w.Hypotheses)
	w.Hypotheses[i] = h
	w.Revision++
	s.putWork(w.ID, w)
	s.emit(HypothesisResolved, actor, w, false)
	return HypothesisReceipt{WorkID: w.ID, WorkRevision: w.Revision, Hypothesis: h.Clone(), Unresolved: unresolved(w)}, nil
}

// checkRun admits ref as evidence for h: a run this assignment made after h
// was recorded.
func (s *Store) checkRun(actor identity.ActorID, w Work, h Hypothesis, ref string) error {
	if !strings.HasPrefix(ref, "execution:") {
		return invalid(fmt.Sprintf("%s is not a run; cite the evidence_ref a shell or run_trials result returned", ref))
	}
	if s.evidenceLookup == nil {
		return fmt.Errorf("%w: this session does not record runs", ErrNotFound)
	}
	run, err := s.evidenceLookup(ref)
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: no run %s; cite an evidence_ref a shell or run_trials result returned, never one composed by hand", ErrNotFound, ref)
	}
	if err != nil {
		return fmt.Errorf("run %s: %w", ref, err)
	}
	if run.WorkID != w.ID || run.AssignedAtRevision != w.AssignedAtRevision || run.Actor != actor {
		return fmt.Errorf("%w: run %s belongs to another assignment; cite runs you made for %s", ErrForbidden, ref, w.ID)
	}
	if s.sequence[ref] <= s.sequence[string(h.ID)] {
		return fmt.Errorf("%w: run %s started before %s was recorded; a hypothesis is tested only by runs made after it, so run the measurement again", ErrState, ref, h.ID)
	}
	return nil
}

func unresolved(w Work) []HypothesisID {
	var out []HypothesisID
	for _, h := range w.Hypotheses {
		if h.Result == nil {
			out = append(out, h.ID)
		}
	}
	return out
}

// SubmitExperiment delivers the conclusion once every hypothesis has a
// result.
func (s *Store) SubmitExperiment(actor identity.ActorID, r SubmitExperimentRequest) (result SubmitExperimentResult, err error) {
	if err = s.beginMutation(); err != nil {
		return result, err
	}
	defer s.endMutation(&err)
	w, err := s.target(actor, r.WorkTarget, false)
	if err != nil {
		return result, err
	}
	if w.Kind != Experiment || w.State != Active {
		return result, ErrState
	}
	if len(w.Hypotheses) == 0 {
		return result, invalid("an experiment delivers hypotheses and their results; record one with record_hypothesis first")
	}
	if open := unresolved(w); len(open) > 0 {
		return result, fmt.Errorf("%w: %v have no result; record each with record_result (inconclusive when the measurement settled nothing)", ErrState, open)
	}
	if blank(r.Summary) || blank(r.Method.ReproduceCommand) || len(r.ProposedSteps) > 32 || len(r.Method.Files) > 16 {
		return result, invalid("summary and method.reproduce_command required; at most 16 method files and 32 proposed steps")
	}
	if err = prose(r.Summary, r.Recommendation, r.Method.ReproduceCommand); err != nil {
		return result, err
	}
	for _, step := range r.ProposedSteps {
		if blank(step.Title) {
			return result, invalid("proposed step title required")
		}
		if err = prose(append([]string{step.Title}, step.AcceptanceCriteria...)...); err != nil {
			return result, err
		}
	}
	c := Conclusion{ID: ConclusionID(s.id("conclusion")), Author: actor, RecordedAt: time.Now().UTC(), WorkID: w.ID, WorkRevision: w.Revision + 1, AssignedAtRevision: w.AssignedAtRevision, Summary: r.Summary, Hypotheses: cloneHypotheses(w.Hypotheses), Method: r.Method, Recommendation: r.Recommendation, ProposedSteps: r.ProposedSteps}.Clone()
	encoded, err := json.Marshal(c)
	if err != nil {
		return result, err
	}
	if len(encoded) > 64*1024 {
		return result, invalid("encoded conclusion exceeds 64 KiB; name fewer or smaller method files")
	}
	w.State, w.Revision, w.LatestConclusionID, w.Blocker = Delivered, c.WorkRevision, c.ID, ""
	s.settleScope(w, Completed)
	s.conclusions[c.ID] = c
	s.change.Conclusions = append(s.change.Conclusions, c.Clone())
	s.putWork(w.ID, w)
	s.emit(ExperimentDelivered, actor, w, true)
	return SubmitExperimentResult{WorkID: w.ID, WorkRevision: w.Revision, State: Delivered, ConclusionID: c.ID, RecordedAt: c.RecordedAt}, nil
}

// GetConclusion reads a delivered conclusion. Any agent may: its method is
// what an implementor adds and an auditor reruns to check a fix.
func (s *Store) GetConclusion(actor identity.ActorID, id ConclusionID) (Conclusion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conclusions[id]
	if !ok {
		if hint, ok := Misrouted(string(id), "conclusion-"); ok {
			return Conclusion{}, fmt.Errorf("%w: %s", ErrNotFound, hint)
		}
		return Conclusion{}, fmt.Errorf("%w: conclusion %s; %s", ErrNotFound, id, s.knownConclusions(actor))
	}
	if actor == "" {
		return Conclusion{}, ErrForbidden
	}
	return c.Clone(), nil
}
func (v *ReadModel) GetConclusion(actor identity.ActorID, id ConclusionID) (Conclusion, error) {
	return v.store.GetConclusion(actor, id)
}

func (s *Store) knownConclusions(actor identity.ActorID) string {
	var delivered []string
	for _, c := range s.conclusions {
		delivered = append(delivered, fmt.Sprintf("%s (%s)", c.ID, c.WorkID))
	}
	return known("delivered conclusions", "no conclusion has been delivered to you", delivered)
}

// order stamps an id with the store's issue sequence; runs and hypotheses
// share it, so a run can be compared with the hypothesis it is cited for.
func (s *Store) order(id string) {
	if s.sequence == nil {
		s.sequence = map[string]uint64{}
	}
	s.issuedCount++
	s.sequence[id] = s.issuedCount
}
