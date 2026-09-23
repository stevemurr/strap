package research

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stevemurr/strap/provider"
)

type Engine struct {
	config     Config
	model      provider.Provider
	modelSlots chan struct{}
	mu         sync.Mutex
	active     bool
	retained   int
}

type Dependencies struct {
	Web    Retrieval
	Record Recorder
	// Check revalidates the captured assignment before external operations.
	Check func(context.Context) error
}

func New(config Config, model provider.Provider) (*Engine, error) {
	c, err := config.Resolve()
	if err != nil {
		return nil, err
	}
	if model == nil {
		return nil, errors.New("research requires a provider")
	}
	for _, l := range []Limits{c.Survey, c.Standard, c.Exhaustive} {
		if l.Tokens > 0 {
			if err := tokenCapabilities(model); err != nil {
				return nil, err
			}
		}
	}
	return &Engine{config: c, model: model, modelSlots: make(chan struct{}, c.ModelConcurrency)}, nil
}
func (e *Engine) Configuration() Config { return e.config }
func tokenCapabilities(p provider.Provider) error {
	_, count := p.(provider.TokenCounter)
	l, limit := p.(provider.OutputTokenLimiter)
	if !count || !limit || l.OutputTokenLimit() == nil || *l.OutputTokenLimit() < 1 {
		return errors.New("a hard research token budget requires input counting and a configured output token cap")
	}
	return nil
}
func (e *Engine) Validate(req Request) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if e.config.limits(req).Tokens > 0 {
		return tokenCapabilities(e.model)
	}
	return nil
}

type fetchFlight struct {
	done   chan struct{}
	source Source
	err    error
}
type run struct {
	engine              *Engine
	request             Request
	binding             Binding
	deps                Dependencies
	limits              Limits
	policy              domainPolicy
	ctx                 context.Context
	cancel              context.CancelFunc
	id                  string
	started, deadline   time.Time
	mu                  sync.Mutex
	spend               Spend
	sourceMu            sync.Mutex
	sources             map[string]Source
	urls                map[string]*fetchFlight
	claims              map[string]Claim
	limitations         []string
	recordMu            sync.Mutex
	sequence, completed uint64
	recordErr           error
}

func (e *Engine) Run(ctx context.Context, b Binding, req Request, deps Dependencies) (Report, error) {
	if err := e.Validate(req); err != nil {
		return Report{}, err
	}
	if deps.Web == nil || deps.Record == nil || b.WorkID != req.WorkID || b.Actor == "" || b.Assignment == 0 || b.InvocationID == "" {
		return Report{}, errors.New("research requires bound retrieval and recording")
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	reserve := 2 * (e.config.MaxReportBytes + 8192)
	e.mu.Lock()
	if e.active {
		e.mu.Unlock()
		return Report{}, ErrBusy
	}
	if reserve > e.config.MaxSessionBytes-e.retained {
		e.mu.Unlock()
		return Report{}, ErrRetention
	}
	e.active = true
	e.retained += reserve
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.active = false; e.mu.Unlock() }()
	limits := e.config.limits(req)
	started := time.Now().UTC()
	deadline := started.Add(limits.Duration)
	runctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	var nonce [16]byte
	rand.Read(nonce[:])
	policy, _ := newPolicy(req.AllowDomains, req.BlockDomains)
	r := &run{engine: e, request: clone(req), binding: b, deps: deps, limits: limits, policy: policy, ctx: runctx, cancel: cancel, id: "run-" + hex.EncodeToString(nonce[:]), started: started, deadline: deadline, sources: map[string]Source{}, urls: map[string]*fetchFlight{}, claims: map[string]Claim{}}
	initial := boundReport(r.snapshot(), e.config.MaxReportBytes)
	initial.Status = "running"
	if err := r.emit(Event{Kind: "started", Stage: "plan", Report: &initial}, false); err != nil {
		return Report{}, err
	}
	acquire, stopAcquire := context.WithDeadline(runctx, started.Add(limits.Duration*3/4))
	defer stopAcquire()
	runErr := r.investigate(acquire)
	stopAcquire()
	report := r.snapshot()
	if runctx.Err() == nil && len(report.Claims) > 0 {
		var err error
		report, err = r.finish(runctx, report)
		if err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	if runctx.Err() != nil {
		runErr = errors.Join(runErr, context.Cause(runctx))
	}
	// Only independently checked claims belong in a terminal report's claims.
	var accepted []Claim
	for _, f := range report.Claims {
		if supported(f) {
			accepted = append(accepted, f)
		} else {
			report.Rejected = append(report.Rejected, f)
		}
	}
	report.Claims = accepted
	report.FinishedAt = time.Now().UTC()
	report.Spend = r.accounting()
	report.Spend.Elapsed = report.FinishedAt.Sub(started)
	report.Status = "partial"
	report.StopReason = reason(runErr)
	complete := len(accepted) > 0 && len(report.Coverage) == len(req.SuccessCriteria)+len(req.MustCover)
	for _, c := range report.Coverage {
		if c.Status != "met" {
			complete = false
		}
	}
	if complete && runctx.Err() == nil && runErr == nil {
		report.Status = "complete"
		report.StopReason = "completed"
	}
	if len(report.Sources) == 0 {
		report.Status = "failed"
	}
	if runErr != nil {
		report.Limitations = append(report.Limitations, clip(runErr.Error(), 2048))
	}
	if len(accepted) > 0 && strings.TrimSpace(report.Summary) == "" {
		summaries := map[string]Claim{}
		ids := []string{}
		for _, f := range accepted {
			summaries[f.ID] = f
			ids = append(ids, f.ID)
		}
		report.Summary = joinClaims(ids, summaries)
	}
	if len(accepted) == 0 {
		report.Summary = "No verified conclusion was established. Retained sources and unresolved claims are available for inspection."
		report.Recommendation = ""
	}
	report = boundReport(report, e.config.MaxReportBytes)
	if err := r.emit(Event{Kind: "finished", Stage: "finished", Report: &report}, true); err != nil {
		return report, err
	}
	return report, nil
}

func reason(err error) string {
	switch {
	case errors.Is(err, ErrReassigned):
		return "reassigned"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, ErrTokens):
		return "token_budget"
	case errors.Is(err, ErrRequests):
		return "request_budget"
	case errors.Is(err, ErrRetention):
		return "retention_budget"
	case errors.Is(err, ErrOutput):
		return "output_limit"
	case errors.Is(err, ErrModel):
		return "invalid_model_output"
	case err != nil:
		return "provider_error"
	default:
		return "insufficient_evidence"
	}
}
func (r *run) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.deps.Check != nil {
		return r.deps.Check(ctx)
	}
	return nil
}
func (r *run) accounting() Spend { r.mu.Lock(); defer r.mu.Unlock(); return r.spend }
func (r *run) note(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.limitations) < 64 {
		r.limitations = append(r.limitations, clip(s, 1024))
	}
}
func (r *run) snapshot() Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := Report{ID: r.id, Version: Version, PromptVersion: PromptVersion, Binding: r.binding, Question: r.request.Question, StartedAt: r.started, Limits: r.limits, Spend: r.spend, Status: "running", Limitations: append([]string{}, r.limitations...)}
	for _, s := range r.sources {
		s.Text = ""
		p.Sources = append(p.Sources, s)
	}
	for _, f := range r.claims {
		p.Claims = append(p.Claims, clone(f))
	}
	sort.Slice(p.Sources, func(i, j int) bool { return p.Sources[i].ID < p.Sources[j].ID })
	sort.Slice(p.Claims, func(i, j int) bool { return p.Claims[i].ID < p.Claims[j].ID })
	for i, s := range append(append([]string{}, r.request.SuccessCriteria...), r.request.MustCover...) {
		p.Coverage = append(p.Coverage, Coverage{Index: i, Requirement: s, Status: "unmet", Reason: "Not yet verified"})
	}
	return p
}
func (r *run) emit(e Event, completed bool) error {
	r.recordMu.Lock()
	defer r.recordMu.Unlock()
	if r.recordErr != nil {
		return r.recordErr
	}
	e.RunID = r.id
	e.Binding = r.binding
	e.Sequence = r.sequence + 1
	e.At = time.Now().UTC()
	e.Deadline = r.deadline
	if completed {
		r.completed++
	}
	e.Completed = r.completed
	if err := e.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if e.Kind != "finished" {
		r.engine.mu.Lock()
		fits := len(raw)+1024 <= r.engine.config.MaxSessionBytes-r.engine.retained
		if fits {
			r.engine.retained += len(raw) + 1024
		}
		r.engine.mu.Unlock()
		if !fits {
			return ErrRetention
		}
	}
	// Recording completion must survive execution cancellation, but is still bounded.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.ctx), r.engine.config.SettlementTimeout)
	defer cancel()
	if err = r.deps.Record(ctx, clone(e)); err != nil {
		r.recordErr = fmt.Errorf("retain research record: %w", err)
		r.cancel()
		return r.recordErr
	}
	r.sequence++
	return nil
}

func boundReport(p Report, limit int) Report {
	raw, _ := json.Marshal(p)
	if len(raw) <= limit {
		return p
	}
	p.Truncated = true
	p.Status = "partial"
	p.StopReason = "retention_budget"
	p.Summary = "Report exceeded the retention limit. Read retained source records and earlier checkpoints for the investigation."
	p.Recommendation = ""
	p.Disagreements = nil
	p.OpenQuestions = nil
	p.Rejected = nil
	p.Claims = nil
	p.Limitations = []string{"Full report omitted at the report byte limit; source records remain available."}
	for i := range p.Coverage {
		p.Coverage[i].Status = "unmet"
		p.Coverage[i].ClaimIDs = nil
		p.Coverage[i].Reason = "Claims omitted at report limit"
	}
	for {
		raw, _ = json.Marshal(p)
		if len(raw) <= limit {
			return p
		}
		if len(p.Sources) > 0 {
			p.Sources = p.Sources[:len(p.Sources)-1]
		} else if len(p.Coverage) > 0 {
			p.Coverage = p.Coverage[:len(p.Coverage)-1]
		} else {
			p.Question = clip(p.Question, 256)
			return p
		}
	}
}

// Digest is model-facing and intentionally excludes source bodies and rejected claims.
func Digest(p Report) json.RawMessage {
	v := struct {
		ID         string     `json:"run_id"`
		Status     string     `json:"status"`
		StopReason string     `json:"stop_reason"`
		Summary    string     `json:"summary"`
		Reader     string     `json:"reader"`
		Coverage   []Coverage `json:"coverage"`
		Spend      Spend      `json:"spend"`
		Truncated  bool       `json:"truncated"`
	}{p.ID, p.Status, p.StopReason, clip(p.Summary, 4096), "Read claims and sources with get_research_run using this run_id. To deliver a claim, record it with report_work_progress; submit_research cites the finding IDs that call returns.", p.Coverage, p.Spend, false}
	for {
		raw, _ := json.Marshal(v)
		if len(raw) <= 12<<10 {
			return raw
		}
		v.Truncated = true
		if len(v.Coverage) > 0 {
			v.Coverage = v.Coverage[:len(v.Coverage)-1]
		} else {
			v.Summary = clip(v.Summary, 1024)
		}
	}
}

func requirementInput(r *run) map[string]any {
	return map[string]any{"question": r.request.Question, "context": r.request.Context, "success_criteria": r.request.SuccessCriteria, "must_cover": r.request.MustCover}
}
func supported(f Claim) bool {
	return f.Basis == "observed" && f.Verdict == "supported" || f.Basis == "inferred" && f.Verdict == "premises_supported"
}
func joinClaims(ids []string, claims map[string]Claim) string {
	var lines []string
	for _, id := range ids {
		if f, ok := claims[id]; ok && supported(f) {
			s := f.Claim
			if f.Basis == "inferred" {
				s = "Inference: " + s + " (" + f.Limitation + ")"
			}
			lines = append(lines, s+" ["+id+"]")
		}
	}
	return strings.Join(lines, "\n\n")
}
