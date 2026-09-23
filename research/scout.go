package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type question struct {
	Question  string `json:"question"`
	Query     string `json:"query"`
	DependsOn []int  `json:"depends_on"`
}
type plan struct {
	Questions []question `json:"questions"`
}

const planShape = `{"questions":[{"question":"bounded subquestion","query":"initial search terms","depends_on":[]}]} Each dependency is a zero-based index of an earlier question. At most the supplied limit. Use independent questions only when useful. An empty questions array means available evidence is sufficient.`

func validatePlan(p plan, limit int) error {
	if len(p.Questions) > limit {
		return errors.New("too many subquestions")
	}
	for i, q := range p.Questions {
		if strings.TrimSpace(q.Question) == "" || len(q.Question) > 4096 || strings.TrimSpace(q.Query) == "" || len(q.Query) > 8192 {
			return errors.New("question and query required and bounded")
		}
		for _, d := range q.DependsOn {
			if d < 0 || d >= i {
				return errors.New("dependencies must name earlier questions")
			}
		}
	}
	return nil
}
func (r *run) investigate(ctx context.Context) error {
	input := requirementInput(r)
	input["limit"] = r.limits.Subquestions
	p, err := stage[plan](ctx, r, "plan", planShape, input, false, func(p plan) error { return validatePlan(p, r.limits.Subquestions) })
	if err != nil {
		return err
	}
	if len(p.Questions) == 0 {
		return nil
	}
	var runErrors []error
	for round := 0; round <= r.limits.Rounds; round++ {
		before := len(r.snapshot().Claims)
		if err := r.runScouts(ctx, p.Questions); err != nil {
			runErrors = append(runErrors, err)
		}
		snap := r.snapshot()
		snap = boundReport(snap, r.engine.config.MaxReportBytes)
		snap.Status = "running"
		if err := r.emit(Event{Kind: "checkpoint", Stage: "reconcile", Report: &snap}, true); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return errors.Join(append(runErrors, ctx.Err())...)
		}
		if round == r.limits.Rounds || len(snap.Claims) == before {
			break
		}
		input := requirementInput(r)
		input["limit"] = r.limits.Subquestions
		input["claims"] = snap.Claims
		input["limitations"] = snap.Limitations
		p, err = stage[plan](ctx, r, "reconcile", planShape+" Propose follow-up only for important unanswered criteria or unresolved conflicting sources. Do not repeat completed searches.", input, false, func(p plan) error { return validatePlan(p, r.limits.Subquestions) })
		if err != nil {
			runErrors = append(runErrors, err)
			break
		}
		if len(p.Questions) == 0 {
			break
		}
	}
	return errors.Join(runErrors...)
}

func (r *run) runScouts(ctx context.Context, questions []question) error {
	slots := make(chan struct{}, r.limits.Scouts)
	done := make([]chan struct{}, len(questions))
	errs := make([]error, len(questions))
	var wg sync.WaitGroup
	for i := range done {
		done[i] = make(chan struct{})
	}
	for i, q := range questions {
		wg.Add(1)
		go func(i int, q question) {
			defer wg.Done()
			defer close(done[i])
			for _, dep := range q.DependsOn {
				select {
				case <-done[dep]:
				case <-ctx.Done():
					errs[i] = ctx.Err()
					return
				}
			}
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-slots }()
			errs[i] = r.scout(ctx, i, q)
		}(i, q)
	}
	wg.Wait()
	return errors.Join(errs...)
}

type candidate struct {
	Claim      string              `json:"claim"`
	Basis      string              `json:"basis"`
	Evidence   []candidateCitation `json:"evidence"`
	Limitation string              `json:"limitation"`
}
type candidateCitation struct {
	SourceID string `json:"source_id"`
	Quote    string `json:"quote"`
}
type scoutAction struct {
	Action      string      `json:"action"`
	Query       string      `json:"query"`
	URL         string      `json:"url"`
	Claims      []candidate `json:"claims"`
	Status      string      `json:"status"`
	Limitations []string    `json:"limitations"`
}

const scoutShape = `{"action":"search|read|finish","query":"search terms or empty","url":"a URL from the supplied hits/links or empty","claims":[{"claim":"one atomic claim","basis":"observed|inferred","evidence":[{"source_id":"provided source_id","quote":"short exact unique excerpt"}],"limitation":"required for inference"}],"status":"complete|partial|failed or empty while continuing","limitations":[]} Extract evidence from the last observation before choosing the next action. Never use search snippets as evidence. Finish when the subquestion is answered or no useful next step remains. Quotations must be 1..1000 UTF-8 bytes and unique within the source. At most 16 claims per action, 8 references per claim. Inferred recommendations must state assumptions and limitations.`

func (r *run) scout(ctx context.Context, id int, q question) error {
	allowed := map[string]bool{}
	seenQueries := map[string]bool{}
	hits, err := r.search(ctx, q.Query)
	if err != nil {
		return err
	}
	seenQueries[q.Query] = true
	for _, h := range hits {
		allowed[h.URL] = true
	}
	var observation any = map[string]any{"hits": hits}
	var localClaims []Claim
	for step := 0; step < r.limits.ModelCalls; step++ {
		input := requirementInput(r)
		input["subquestion"] = q.Question
		input["observation"] = observation
		input["claims"] = localClaims
		// Dependency conclusions are compact; source text is supplied only on reads.
		input["known_claims"] = r.snapshot().Claims
		action, err := stage[scoutAction](ctx, r, "scout", scoutShape, input, false, func(a scoutAction) error {
			if a.Action != "search" && a.Action != "read" && a.Action != "finish" {
				return errors.New("unknown scout action")
			}
			if len(a.Claims) > 16 || len(a.Limitations) > 8 {
				return errors.New("scout result exceeds item limits")
			}
			if a.Action == "finish" && a.Status != "complete" && a.Status != "partial" && a.Status != "failed" {
				return errors.New("finish requires explicit status")
			}
			for _, f := range a.Claims {
				if _, err := r.claim(f); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, s := range action.Limitations {
			r.note(s)
		}
		for _, c := range action.Claims {
			f, err := r.claim(c)
			if err != nil {
				return err
			}
			r.mu.Lock()
			_, exists := r.claims[f.ID]
			if !exists && len(r.claims) < 128 {
				r.claims[f.ID] = f
			}
			r.mu.Unlock()
			if !exists && len(localClaims) < 32 {
				localClaims = append(localClaims, f)
			}
		}
		if err := r.emit(Event{Kind: "progress", Stage: "scout: step completed", Scout: id + 1}, true); err != nil {
			return err
		}
		switch action.Action {
		case "finish":
			if action.Status != "complete" {
				r.note(fmt.Sprintf("Subquestion %q ended %s", q.Question, action.Status))
			}
			return nil
		case "search":
			if seenQueries[action.Query] {
				r.note("Repeated search skipped: " + clip(action.Query, 200))
				return nil
			}
			seenQueries[action.Query] = true
			hits, err := r.search(ctx, action.Query)
			if err != nil {
				return err
			}
			for _, h := range hits {
				allowed[h.URL] = true
			}
			observation = map[string]any{"hits": hits}
		case "read":
			url, err := canonical(action.URL)
			if err != nil || !allowed[url] {
				return fmt.Errorf("%w: read URL was not supplied by retrieval", ErrModel)
			}
			s, err := r.fetch(ctx, url)
			if err != nil {
				if ctx.Err() != nil || errors.Is(err, ErrRetention) || errors.Is(err, ErrRequests) {
					return err
				}
				r.note("Cannot read " + url + ": " + clip(err.Error(), 300))
				observation = map[string]any{"error": clip(err.Error(), 512), "url": url}
				delete(allowed, url)
				continue
			}
			// The retained text is exactly what the scout receives. Fetching a source
			// never depends on the browser cache remaining alive during verification.
			observation = map[string]any{"source": s}
		}
	}
	return ErrRequests
}
func (r *run) search(ctx context.Context, query string) ([]Hit, error) {
	if strings.TrimSpace(query) == "" || len(query) > 8192 {
		return nil, fmt.Errorf("%w: search query invalid", ErrModel)
	}
	if err := r.check(ctx); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.spend.Searches >= r.limits.Searches {
		r.mu.Unlock()
		return nil, ErrRequests
	}
	r.spend.Searches++
	r.mu.Unlock()
	if err := r.emit(Event{Kind: "progress", Stage: "search"}, false); err != nil {
		return nil, err
	}
	hits, err := r.deps.Web.Search(ctx, query)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	out := []Hit{}
	seen := map[string]bool{}
	for _, h := range hits {
		url, err := canonical(h.URL)
		if err != nil || !r.policy.permits(url) || seen[url] {
			continue
		}
		seen[url] = true
		h.URL = url
		h.Title = clip(h.Title, 500)
		h.Snippet = clip(h.Snippet, 1500)
		out = append(out, h)
		if len(out) == 10 {
			break
		}
	}
	r.mu.Lock()
	r.spend.SearchSuccesses++
	r.mu.Unlock()
	return out, nil
}
func (r *run) fetch(ctx context.Context, url string) (Source, error) {
	if !r.policy.permits(url) {
		return Source{}, errors.New("source excluded by domain policy")
	}
	if err := r.check(ctx); err != nil {
		return Source{}, err
	}
	r.mu.Lock()
	if f, exists := r.urls[url]; exists {
		r.mu.Unlock()
		select {
		case <-f.done:
			return f.source, f.err
		case <-ctx.Done():
			return Source{}, ctx.Err()
		}
	}
	if r.spend.Fetches >= r.limits.Fetches {
		r.mu.Unlock()
		return Source{}, ErrRequests
	}
	r.spend.Fetches++
	f := &fetchFlight{done: make(chan struct{})}
	r.urls[url] = f
	r.mu.Unlock()
	defer close(f.done)
	f.source, f.err = r.fetchNew(ctx, url)
	return f.source, f.err
}
func (r *run) fetchNew(ctx context.Context, url string) (Source, error) {
	if err := r.emit(Event{Kind: "progress", Stage: "read: " + clip(url, 300)}, false); err != nil {
		return Source{}, err
	}
	p, err := r.deps.Web.Fetch(ctx, url)
	if err != nil {
		return Source{}, err
	}
	if err = ctx.Err(); err != nil {
		return Source{}, err
	}
	final, err := canonical(p.FinalURL)
	if err != nil || !r.policy.permits(final) {
		return Source{}, errors.New("final source URL excluded by domain policy")
	}
	if !strings.HasPrefix(p.ContentType, "text/") || !utf8.ValidString(p.Text) || strings.TrimSpace(p.Text) == "" {
		return Source{}, errors.New("source is not readable UTF-8 text")
	}
	// Keep room for the brief, schema, and previously extracted claims in a model call.
	textLimit := min(r.engine.config.MaxSourceBytes, r.engine.config.MaxRequestBytes/4, 24000)
	text := clip(p.Text, textLimit)
	s := Source{URL: url, FinalURL: final, Title: clip(p.Title, 500), ContentType: clip(p.ContentType, 256), FetchedAt: time.Now().UTC(), Text: text, Bytes: len(text), SHA256: hash(text), Truncated: p.Truncated || len(text) < len(p.Text)}
	s.ID = "source-" + hash(final + "\n" + s.SHA256)[:24]
	r.sourceMu.Lock()
	defer r.sourceMu.Unlock()
	r.mu.Lock()
	r.spend.FetchSuccesses++
	if existing, ok := r.sources[s.ID]; ok {
		r.mu.Unlock()
		return existing, nil
	}
	if s.Bytes > r.engine.config.MaxRunSourceBytes-r.spend.SourceBytes {
		r.mu.Unlock()
		return Source{}, ErrRetention
	}
	r.spend.SourceBytes += s.Bytes
	r.mu.Unlock()
	if err := r.emit(Event{Kind: "source", Stage: "source retained", Source: &s}, true); err != nil {
		return Source{}, err
	}
	r.mu.Lock()
	r.sources[s.ID] = s
	r.mu.Unlock()
	return s, nil
}
func (r *run) claim(c candidate) (Claim, error) {
	if strings.TrimSpace(c.Claim) == "" || len(c.Claim) > 2048 || (c.Basis != "observed" && c.Basis != "inferred") || len(c.Evidence) == 0 || len(c.Evidence) > 8 || len(c.Limitation) > 2048 || c.Basis == "inferred" && strings.TrimSpace(c.Limitation) == "" {
		return Claim{}, errors.New("claim requires bounded text, basis, evidence, and inference limitation")
	}
	f := Claim{Claim: c.Claim, Basis: c.Basis, Limitation: c.Limitation, Verdict: "unverified"}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range c.Evidence {
		s, ok := r.sources[e.SourceID]
		if !ok || len(e.Quote) == 0 || len(e.Quote) > 1000 || strings.Count(s.Text, e.Quote) != 1 {
			return Claim{}, errors.New("citation must name a retained source and a unique exact excerpt")
		}
		start := strings.Index(s.Text, e.Quote)
		f.Evidence = append(f.Evidence, Citation{SourceID: s.ID, Quote: e.Quote, Start: start, End: start + len(e.Quote), URI: s.FinalURL, Revision: "sha256:" + s.SHA256, Locator: fmt.Sprintf("%s/%s#bytes=%d-%d", r.id, s.ID, start, start+len(e.Quote))})
	}
	sort.Slice(f.Evidence, func(i, j int) bool { return f.Evidence[i].SourceID < f.Evidence[j].SourceID })
	encoded, _ := json.Marshal(struct {
		Claim, Basis string
		Evidence     []Citation
	}{f.Claim, f.Basis, f.Evidence})
	f.ID = "claim-" + hash(string(encoded))[:24]
	return f, nil
}
