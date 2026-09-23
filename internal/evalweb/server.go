package evalweb

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stevemurr/strap/eval"
	"github.com/stevemurr/strap/eval/interaction"
)

//go:embed ui
var ui embed.FS

// Server answers the results API and serves the embedded page. It reads only
// beneath its root. Jobs and library metadata are written only under that root.
type Server struct {
	root   string
	mux    *http.ServeMux
	runner *Runner

	// mu guards everything below. It is held only for bookkeeping, never
	// across a scan or an analysis, so one slow read does not stall the rest.
	mu         sync.Mutex
	runs       []RunSummary
	indexed    time.Time // when the scan that produced runs started
	generation int       // bumped by invalidate
	scanned    int       // generation the current runs reflect
	refreshing bool
	warming    bool
	reports    map[string]cachedReport
	analyses   map[string]*analysis
	jobs       []*job

	scanning sync.Mutex // serializes discovery
}

type cachedReport struct {
	stamp  string
	report *ladderDetail
}

// analysis is one in-flight eval.Analyze that concurrent requests share.
type analysis struct {
	stamp  string
	done   chan struct{}
	detail *ladderDetail
	err    error
}

// ladderDetail is the analysed run plus the raw results the analysis drops:
// replies, grade output and errors.
type ladderDetail struct {
	Summary RunSummary             `json:"summary"`
	Report  eval.Report            `json:"report"`
	Results map[string]eval.Result `json:"results"`
}

type interactionDetail struct {
	Summary RunSummary         `json:"summary"`
	Report  interaction.Report `json:"report"`
}

// indexTTL bounds how stale the run list may be before a request triggers a
// background rescan. A running eval appends results continuously, so the list
// refreshes on its own without making any request wait for the walk.
const indexTTL = 15 * time.Second

// warmRuns is how many of the newest runs are analysed ahead of the first
// request for them: about one page of the library.
const warmRuns = 10

// New serves the results under root. A nil runner disables launching runs
// from the page.
func New(root string, runner *Runner) (*Server, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}
	s := &Server{root: abs, mux: http.NewServeMux(), runner: runner, reports: map[string]cachedReport{}, analyses: map[string]*analysis{}}
	static, _ := fs.Sub(ui, "ui")
	s.mux.Handle("GET /", http.FileServerFS(static))
	s.mux.HandleFunc("GET /api/runs", s.handleRuns)
	s.mux.HandleFunc("POST /api/library", s.handleLibrary)
	s.mux.HandleFunc("GET /api/run", s.handleRun)
	s.mux.HandleFunc("GET /api/compare", s.handleCompare)
	s.mux.HandleFunc("GET /api/trace", s.handleTrace)
	s.mux.HandleFunc("GET /api/runner", s.handleRunner)
	s.mux.HandleFunc("GET /api/jobs", s.handleJobs)
	s.mux.HandleFunc("POST /api/jobs", s.handleStartJob)
	s.mux.HandleFunc("GET /api/jobs/{id}", s.handleJob)
	s.mux.HandleFunc("GET /api/jobs/{id}/events", s.handleJobEvents)
	s.mux.HandleFunc("POST /api/jobs/{id}/cancel", s.handleCancelJob)
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// Root reports the absolute results directory.
func (s *Server) Root() string { return s.root }

// index returns the run list with live jobs overlaid. A list past its TTL is
// served as is while one rescan runs in the background; the first list, a
// forced one and one invalidated by a change this server made wait for a
// rescan. Discovery never holds s.mu.
func (s *Server) index(force bool) ([]RunSummary, error) {
	s.mu.Lock()
	runs, stale, dirty := s.runs, time.Since(s.indexed) >= indexTTL, s.scanned != s.generation
	if runs != nil && !force && !dirty && stale && !s.refreshing {
		s.refreshing = true
		go func() {
			_, _ = s.rescan(time.Now())
			s.mu.Lock()
			s.refreshing = false
			s.mu.Unlock()
		}()
	}
	s.mu.Unlock()
	if runs == nil || force || dirty {
		var err error
		if runs, err = s.rescan(time.Now()); err != nil {
			return nil, err
		}
	}
	return s.overlayJobs(runs), nil
}

// rescan walks the results tree unless a scan that started after requested
// already finished, which lets simultaneous callers share one walk.
func (s *Server) rescan(requested time.Time) ([]RunSummary, error) {
	s.scanning.Lock()
	defer s.scanning.Unlock()
	s.mu.Lock()
	if s.runs != nil && s.indexed.After(requested) && s.scanned == s.generation {
		runs := s.runs
		s.mu.Unlock()
		return runs, nil
	}
	generation, started := s.generation, time.Now()
	s.mu.Unlock()
	runs, err := discover(s.root)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.runs, s.indexed, s.scanned = runs, started, generation
	s.mu.Unlock()
	s.warm(runs)
	return runs, nil
}

// invalidate makes the next index call wait for a fresh scan. Callers that
// already hold s.mu bump s.generation directly.
func (s *Server) invalidate() {
	s.mu.Lock()
	s.generation++
	s.mu.Unlock()
}

// overlayJobs marks the runs a live job is writing. It copies the list, since
// the cached one is shared between requests.
func (s *Server) overlayJobs(runs []RunSummary) []RunSummary {
	s.mu.Lock()
	jobs := append([]*job(nil), s.jobs...)
	s.mu.Unlock()
	out := runs
	copied := false
	for _, j := range jobs {
		snap := j.snapshot()
		if snap.Status != "running" {
			continue
		}
		if !copied {
			out, copied = append([]RunSummary(nil), runs...), true
		}
		for i := range out {
			if out[i].Path == snap.Dir || out[i].Group == snap.Dir {
				out[i].Archived = false
				out[i].ArchiveReason = ""
				out[i].Status = "running"
				out[i].JobID = snap.ID
			}
		}
	}
	return out
}

// warm analyses the newest ladder runs in the background, one at a time, so
// opening a recent run does not wait for its traces to be read. Runs already
// cached cost a stat each.
func (s *Server) warm(runs []RunSummary) {
	s.mu.Lock()
	if s.warming {
		s.mu.Unlock()
		return
	}
	s.warming = true
	s.mu.Unlock()
	var paths []string
	for _, r := range runs {
		if len(paths) == warmRuns {
			break
		}
		if r.Kind == Ladder && r.Tasks > 0 && !r.Archived && (r.Batch || !batchGroup(runs, r.Group)) {
			paths = append(paths, r.Path)
		}
	}
	go func() {
		defer func() {
			s.mu.Lock()
			s.warming = false
			s.mu.Unlock()
		}()
		for _, p := range paths {
			_, _ = s.ladder(context.Background(), p)
		}
	}()
}

// batchGroup reports whether group is a batch in runs, whose members the
// library shows only through the batch itself.
func batchGroup(runs []RunSummary, group string) bool {
	if group == "" {
		return false
	}
	for _, r := range runs {
		if r.Batch && r.Path == group {
			return true
		}
	}
	return false
}

// locate maps a request path onto a run directory beneath the root, and
// reports whether it is a batch of attempts rather than a single run.
func (s *Server) locate(rel string) (dir string, batch bool, err error) {
	if rel == "" {
		return "", false, errors.New("run is required")
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false, errors.New("run must be a path beneath the results directory")
	}
	dir = filepath.Join(s.root, clean)
	if _, err := os.Stat(filepath.Join(dir, "results.jsonl")); err == nil {
		return dir, false, nil
	}
	if len(batchMembers(dir)) > 0 {
		return dir, true, nil
	}
	return "", false, fmt.Errorf("no run at %s", rel)
}

func (s *Server) summary(rel string) (RunSummary, error) {
	dir, batch, err := s.locate(rel)
	if err != nil {
		return RunSummary{}, err
	}
	if batch {
		return s.batchSummary(dir, rel), nil
	}
	summary := summarize(dir, filepath.ToSlash(filepath.Clean(rel)))
	enrichRun(s.root, &summary)
	return summary, nil
}

// ladder analyses a run once per change of its results file. Analysis walks
// every trace, so the cache is keyed on the results file's size and mtime.
func (s *Server) ladder(ctx context.Context, rel string) (*ladderDetail, error) {
	dir, batch, err := s.locate(rel)
	if err != nil {
		return nil, err
	}
	if batch {
		return s.batchDetail(ctx, dir, rel)
	}
	info, err := os.Stat(filepath.Join(dir, "results.jsonl"))
	if err != nil {
		return nil, err
	}
	stamp := fmt.Sprintf("%d/%d", info.Size(), info.ModTime().UnixNano())
	s.mu.Lock()
	if cached, ok := s.reports[dir]; ok && cached.stamp == stamp {
		s.mu.Unlock()
		return cached.report, nil
	}
	// Requests for the same run share one analysis. It runs detached from the
	// request that started it, so navigating away still fills the cache.
	a := s.analyses[dir]
	if a == nil || a.stamp != stamp {
		a = &analysis{stamp: stamp, done: make(chan struct{})}
		s.analyses[dir] = a
		go s.analyze(context.WithoutCancel(ctx), dir, rel, a)
	}
	s.mu.Unlock()
	select {
	case <-a.done:
		return a.detail, a.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Server) analyze(ctx context.Context, dir, rel string, a *analysis) {
	defer close(a.done)
	defer func() {
		s.mu.Lock()
		if s.analyses[dir] == a {
			delete(s.analyses, dir)
		}
		if a.err == nil {
			s.reports[dir] = cachedReport{stamp: a.stamp, report: a.detail}
		}
		s.mu.Unlock()
	}()
	report, err := eval.Analyze(ctx, dir)
	if err != nil {
		a.err = err
		return
	}
	results, err := latestResults(dir)
	if err != nil {
		a.err = err
		return
	}
	detail := &ladderDetail{Summary: summarize(dir, filepath.ToSlash(filepath.Clean(rel))), Report: report, Results: map[string]eval.Result{}}
	enrichRun(s.root, &detail.Summary)
	for _, r := range results {
		detail.Results[r.TaskID] = r
	}
	a.detail = detail
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.index(r.URL.Query().Has("refresh"))
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"root": s.root, "runs": runs})
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	dir, batch, err := s.locate(rel)
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	if !batch && isInteraction(dir) {
		summary, err := s.summary(rel)
		if err != nil {
			fail(w, http.StatusNotFound, err)
			return
		}
		report, err := interaction.ReadReport(dir)
		if err != nil {
			fail(w, http.StatusUnprocessableEntity, err)
			return
		}
		writeJSON(w, interactionDetail{Summary: summary, Report: report})
		return
	}
	detail, err := s.ladder(r.Context(), rel)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, detail)
}

// handleCompare returns the analysed reports for several ladder runs in the
// order requested; the page builds the task matrix from them.
func (s *Server) handleCompare(w http.ResponseWriter, r *http.Request) {
	paths := r.URL.Query()["run"]
	if len(paths) == 0 {
		fail(w, http.StatusBadRequest, errors.New("at least one run is required"))
		return
	}
	if len(paths) > 12 {
		fail(w, http.StatusBadRequest, errors.New("compare at most 12 runs at once"))
		return
	}
	var out []*ladderDetail
	for _, rel := range paths {
		dir, batch, err := s.locate(rel)
		if err != nil {
			fail(w, http.StatusNotFound, err)
			return
		}
		if !batch && isInteraction(dir) {
			fail(w, http.StatusBadRequest, fmt.Errorf("%s is an interaction run; compare ladder runs", rel))
			return
		}
		detail, err := s.ladder(r.Context(), rel)
		if err != nil {
			fail(w, http.StatusUnprocessableEntity, err)
			return
		}
		out = append(out, detail)
	}
	writeJSON(w, map[string]any{"runs": out})
}

func (s *Server) handleTrace(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	dir, batch, err := s.locate(q.Get("run"))
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	file := filepath.Clean(filepath.FromSlash(q.Get("file")))
	if file == "." || filepath.IsAbs(file) || file == ".." || strings.HasPrefix(file, ".."+string(filepath.Separator)) {
		fail(w, http.StatusBadRequest, errors.New("file must be a trace beneath the run"))
		return
	}
	trace := filepath.Join(dir, file)
	if batch {
		trace = batchTrace(dir, file)
	}
	page, err := readTrace(trace, traceQuery{
		after: atoi(q.Get("after")), limit: atoi(q.Get("limit")),
		kinds: splitList(q.Get("kinds")), agent: q.Get("agent"),
	})
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, page)
}

func atoi(s string) int {
	n := 0
	fmt.Sscanf(s, "%d", &n)
	return n
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	if err := enc.Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func fail(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
