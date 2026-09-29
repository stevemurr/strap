package work

import (
	"errors"
	"strings"
	"testing"

	"github.com/stevemurr/strap/identity"
)

// runs records the evidence refs a test's store issued, as the harness's
// accepted log does.
type runs map[string]ExecutionEvidence

func experimentStore(t *testing.T) (*Store, runs, Work) {
	t.Helper()
	issued := runs{}
	s := New(WithEvidenceLookup(func(ref string) (ExecutionEvidence, error) {
		e, ok := issued[ref]
		if !ok {
			return ExecutionEvidence{}, ErrNotFound
		}
		return e, nil
	}))
	w, err := s.AssignExperiment("manager", ExperimentAssignRequest{Assignee: "experimenter", Task: "Find slow rendering"})
	if err != nil {
		t.Fatal(err)
	}
	return s, issued, w
}

// run issues a run of w by actor.
func (r runs) run(s *Store, w Work, actor identity.ActorID) string {
	ref := s.NewExecutionRef()
	r[ref] = ExecutionEvidence{WorkID: w.ID, AssignedAtRevision: w.AssignedAtRevision, Actor: actor}
	return ref
}

func TestAHypothesisIsTestedOnlyByLaterRuns(t *testing.T) {
	s, issued, w := experimentStore(t)
	before := issued.run(s, w, "experimenter")
	h, err := s.RecordHypothesis("experimenter", RecordHypothesisRequest{WorkID: w.ID, Statement: "Rows re-render on every event", Prediction: "Render time grows linearly with rows", Method: "Time renders at 10, 100 and 1000 rows"})
	if err != nil || len(h.Unresolved) != 1 {
		t.Fatal(h, err)
	}
	id := h.Hypothesis.ID
	if _, err = s.RecordResult("experimenter", RecordResultRequest{WorkID: w.ID, HypothesisID: id, Verdict: Supported, Observed: "10 rows 1ms, 1000 rows 98ms", EvidenceRefs: []string{before}}); !errors.Is(err, ErrState) || !strings.Contains(err.Error(), "started before") {
		t.Fatal("a run from before the hypothesis supported it:", err)
	}
	if _, err = s.RecordResult("experimenter", RecordResultRequest{WorkID: w.ID, HypothesisID: id, Verdict: Supported, Observed: "10 rows 1ms"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("a supported verdict without runs was recorded:", err)
	}
	other := issued.run(s, w, "someone")
	if _, err = s.RecordResult("experimenter", RecordResultRequest{WorkID: w.ID, HypothesisID: id, Verdict: Supported, Observed: "10 rows 1ms", EvidenceRefs: []string{other}}); !errors.Is(err, ErrForbidden) {
		t.Fatal("another actor's run was cited:", err)
	}
	if _, err = s.RecordResult("experimenter", RecordResultRequest{WorkID: w.ID, HypothesisID: id, Verdict: Supported, Observed: "10 rows 1ms", EvidenceRefs: []string{"execution:made-up"}}); !errors.Is(err, ErrNotFound) {
		t.Fatal("a composed ref was cited:", err)
	}
	after := issued.run(s, w, "experimenter")
	got, err := s.RecordResult("experimenter", RecordResultRequest{WorkID: w.ID, HypothesisID: id, Verdict: Supported, Observed: "10 rows 1ms, 1000 rows 98ms", EvidenceRefs: []string{after}})
	if err != nil || got.Hypothesis.Result == nil || len(got.Unresolved) != 0 {
		t.Fatal(got, err)
	}
	if _, err = s.RecordResult("experimenter", RecordResultRequest{WorkID: w.ID, HypothesisID: id, Verdict: Refuted, Observed: "changed my mind", EvidenceRefs: []string{after}}); !errors.Is(err, ErrState) {
		t.Fatal("a result was recorded twice:", err)
	}
}

func TestAnExperimentDeliversOnlyWhenEveryHypothesisHasAResult(t *testing.T) {
	s, issued, w := experimentStore(t)
	submit := func(revision Revision) (SubmitExperimentResult, error) {
		return s.SubmitExperiment("experimenter", SubmitExperimentRequest{WorkTarget: WorkTarget{ID: w.ID, ExpectedRevision: revision}, Summary: "Rendering is linear in rows", Method: ExperimentMethod{ReproduceCommand: "go test -bench Render", Files: []MethodFile{{Path: "render_test.go", Content: "package tui"}}}})
	}
	if _, err := submit(w.Revision); !errors.Is(err, ErrInvalid) {
		t.Fatal("an experiment without hypotheses delivered:", err)
	}
	first, err := s.RecordHypothesis("experimenter", RecordHypothesisRequest{WorkID: w.ID, Statement: "Rows re-render", Prediction: "Linear growth", Method: "Benchmark"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.RecordHypothesis("experimenter", RecordHypothesisRequest{WorkID: w.ID, Statement: "Styles are recomputed", Prediction: "Style time dominates", Method: "Profile"})
	if err != nil {
		t.Fatal(err)
	}
	ref := issued.run(s, w, "experimenter")
	if _, err = s.RecordResult("experimenter", RecordResultRequest{WorkID: w.ID, HypothesisID: first.Hypothesis.ID, Verdict: Refuted, Observed: "Flat at 1ms", EvidenceRefs: []string{ref}}); err != nil {
		t.Fatal(err)
	}
	current := s.works[w.ID].Revision
	if _, err = submit(current); !errors.Is(err, ErrState) || !strings.Contains(err.Error(), string(second.Hypothesis.ID)) {
		t.Fatal("an experiment with an open hypothesis delivered:", err)
	}
	// Inconclusive needs no runs: the measurement settled nothing.
	resolved, err := s.RecordResult("experimenter", RecordResultRequest{WorkID: w.ID, HypothesisID: second.Hypothesis.ID, Verdict: Inconclusive, Observed: "Profiler unavailable"})
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := submit(resolved.WorkRevision)
	if err != nil || delivered.State != Delivered {
		t.Fatal(delivered, err)
	}
	if _, err = s.RecordHypothesis("experimenter", RecordHypothesisRequest{WorkID: w.ID, Statement: "late", Prediction: "p", Method: "m"}); !errors.Is(err, ErrState) {
		t.Fatal("a hypothesis was added after delivery:", err)
	}
	// Any agent reads a conclusion: its method is what an implementor adds.
	c, err := s.GetConclusion("implementor", delivered.ConclusionID)
	if err != nil || len(c.Hypotheses) != 2 || c.Hypotheses[1].Result.Verdict != Inconclusive || c.Method.Files[0].Content != "package tui" {
		t.Fatal(c, err)
	}
	if _, err = s.GetConclusion("implementor", ConclusionID(w.ID)); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "get_work") {
		t.Fatal("a work id read as a conclusion did not name the right reader:", err)
	}
	// The accepted change carries the conclusion to a passive read model.
	view := NewReadModel()
	for _, e := range s.PendingEvents(0) {
		view.Apply(e.Changes())
	}
	if got, err := view.GetConclusion("manager", delivered.ConclusionID); err != nil || got.Summary != "Rendering is linear in rows" {
		t.Fatal(got, err)
	}
	if got, err := view.GetWork("manager", w.ID); err != nil || len(got.Hypotheses) != 2 || got.LatestConclusionID != delivered.ConclusionID {
		t.Fatal(got, err)
	}
}

func TestOnlyTheExperimenterRecordsHypotheses(t *testing.T) {
	s, _, w := experimentStore(t)
	if _, err := s.RecordHypothesis("manager", RecordHypothesisRequest{WorkID: w.ID, Statement: "s", Prediction: "p", Method: "m"}); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	research, err := s.AssignInvestigation("manager", InvestigationRequest{Kind: WebResearch, Assignee: "experimenter", Task: "Look it up"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordHypothesis("experimenter", RecordHypothesisRequest{WorkID: research.ID, Statement: "s", Prediction: "p", Method: "m"}); !errors.Is(err, ErrState) {
		t.Fatal("a hypothesis was recorded on research:", err)
	}
	if _, err = s.RecordHypothesis("experimenter", RecordHypothesisRequest{WorkID: w.ID, Statement: "s", Prediction: " ", Method: "m"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("a hypothesis without a prediction was recorded:", err)
	}
	if _, err = s.Cancel("manager", CancelRequest{WorkTarget: WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Reason: "withdrawn"}); err != nil {
		t.Fatal("an experiment could not be cancelled:", err)
	}
}
