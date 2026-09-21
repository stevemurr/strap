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
// beneath its root and never writes.
type Server struct {
	root   string
	mux    *http.ServeMux
	runner *Runner

	mu      sync.Mutex
	runs    []RunSummary
	indexed time.Time
	reports map[string]cachedReport
	jobs    []*job
}

type cachedReport struct {
	stamp  string
	report *ladderDetail
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

// indexTTL bounds how stale the run list may be between rescans. A running
// eval appends results continuously, so the list refreshes on its own.
const indexTTL = 15 * time.Second

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
	s := &Server{root: abs, mux: http.NewServeMux(), runner: runner, reports: map[string]cachedReport{}}
	static, _ := fs.Sub(ui, "ui")
	s.mux.Handle("GET /", http.FileServerFS(static))
	s.mux.HandleFunc("GET /api/runs", s.handleRuns)
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

func (s *Server) index(force bool) ([]RunSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !force && s.runs != nil && time.Since(s.indexed) < indexTTL {
		return s.runs, nil
	}
	runs, err := discover(s.root)
	if err != nil {
		return nil, err
	}
	s.runs, s.indexed = runs, time.Now()
	return runs, nil
}

// resolve maps a request path onto a run directory beneath the root.
func (s *Server) resolve(rel string) (string, error) {
	if rel == "" {
		return "", errors.New("run is required")
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("run must be a path beneath the results directory")
	}
	dir := filepath.Join(s.root, clean)
	if _, err := os.Stat(filepath.Join(dir, "results.jsonl")); err != nil {
		return "", fmt.Errorf("no run at %s", rel)
	}
	return dir, nil
}

func (s *Server) summary(rel string) (RunSummary, error) {
	dir, err := s.resolve(rel)
	if err != nil {
		return RunSummary{}, err
	}
	return summarize(dir, filepath.ToSlash(filepath.Clean(rel))), nil
}

// ladder analyses a run once per change of its results file. Analysis walks
// every trace, so the cache is keyed on the results file's size and mtime.
func (s *Server) ladder(ctx context.Context, rel string) (*ladderDetail, error) {
	dir, err := s.resolve(rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(filepath.Join(dir, "results.jsonl"))
	if err != nil {
		return nil, err
	}
	stamp := fmt.Sprintf("%d/%d", info.Size(), info.ModTime().UnixNano())
	s.mu.Lock()
	cached, ok := s.reports[dir]
	s.mu.Unlock()
	if ok && cached.stamp == stamp {
		return cached.report, nil
	}
	report, err := eval.Analyze(ctx, dir)
	if err != nil {
		return nil, err
	}
	results, err := latestResults(dir)
	if err != nil {
		return nil, err
	}
	detail := &ladderDetail{Summary: summarize(dir, filepath.ToSlash(filepath.Clean(rel))), Report: report, Results: map[string]eval.Result{}}
	for _, r := range results {
		detail.Results[r.TaskID] = r
	}
	s.mu.Lock()
	s.reports[dir] = cachedReport{stamp: stamp, report: detail}
	s.mu.Unlock()
	return detail, nil
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
	summary, err := s.summary(rel)
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	if summary.Kind == Interaction {
		dir, _ := s.resolve(rel)
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
		summary, err := s.summary(rel)
		if err != nil {
			fail(w, http.StatusNotFound, err)
			return
		}
		if summary.Kind != Ladder {
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
	dir, err := s.resolve(q.Get("run"))
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	file := filepath.Clean(filepath.FromSlash(q.Get("file")))
	if file == "." || filepath.IsAbs(file) || file == ".." || strings.HasPrefix(file, ".."+string(filepath.Separator)) {
		fail(w, http.StatusBadRequest, errors.New("file must be a trace beneath the run"))
		return
	}
	page, err := readTrace(filepath.Join(dir, file), traceQuery{
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
