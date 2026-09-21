package evalweb

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/internal/evalwire"
)

func TestEvalConfigurationIsValidatedAndIsolated(t *testing.T) {
	r, _ := fakeContainer(t, "")
	worker := r.Config.Model
	worker.Model = "worker"
	r.Config.Implementor.Model = &worker
	zero, thinking := 0.0, false
	m := r.Config.Model
	m.Model = "new-model"
	m.Generation.Temperature = &zero
	m.Generation.EnableThinking = &thinking
	configured, err := r.configured(newEval{Model: &m, ApplyToRoles: true})
	if err != nil {
		t.Fatal(err)
	}
	if configured.Config.Implementor.Model != nil || r.Config.Implementor.Model.Model != "worker" {
		t.Fatal("role override policy not isolated")
	}
	*configured.Config.Model.Generation.Temperature = 1
	if *m.Generation.Temperature != 0 {
		t.Fatal("request pointers shared with job")
	}
	retained, err := r.configured(newEval{Model: &m})
	if err != nil {
		t.Fatal(err)
	}
	retained.Config.Implementor.Model.Model = "changed"
	if r.Config.Implementor.Model.Model != "worker" {
		t.Fatal("role config shared with server")
	}
	for _, bad := range []harness.ModelConfig{
		{Backend: "vllm", Model: "", BaseURL: m.BaseURL, Timeout: time.Minute},
		{Backend: "vllm", Model: "x", BaseURL: "not a URL", Timeout: time.Minute},
		{Backend: "vllm", Model: "x", BaseURL: m.BaseURL},
	} {
		if _, err := r.configured(newEval{Model: &bad}); err == nil {
			t.Fatalf("accepted invalid model: %+v", bad)
		}
	}
	high := 3.0
	m.Generation.Temperature = &high
	if _, err := r.configured(newEval{Model: &m}); err == nil {
		t.Fatal("accepted invalid temperature")
	}
	m.Generation.Temperature = &zero
	m.Backend = "chatcompletions"
	if _, err := r.configured(newEval{Model: &m}); err == nil {
		t.Fatal("silently dropped generation settings")
	}
}

func TestNamedRunPersistsConfigurationIntoContainerSnapshot(t *testing.T) {
	r, _ := fakeContainer(t, "")
	srv, err := New(t.TempDir(), r)
	if err != nil {
		t.Fatal(err)
	}
	m := r.Config.Model
	temp, thinking := 0.0, false
	m.Generation.Temperature = &temp
	m.Generation.EnableThinking = &thinking
	j, err := srv.startJob([]string{"easy-01-budget-pair"}, newEval{Name: "Greedy baseline", Model: &m, ApplyToRoles: true})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for j.snapshot().Status == "running" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if j.snapshot().Status == "running" {
		j.cancel()
		t.Fatal("job did not complete")
	}
	b, err := os.ReadFile(filepath.Join(srv.root, j.dir, "config", "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	config, err := evalwire.ParseConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	if config.Harness.Model.Generation.EnableThinking == nil || *config.Harness.Model.Generation.EnableThinking || *config.Harness.Model.Generation.Temperature != 0 {
		t.Fatal("explicit zero/false did not reach container")
	}
	fresh, _ := New(srv.root, nil)
	runs, err := fresh.index(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if run.Path == j.dir {
			if run.DisplayName != "Greedy baseline" || run.Configuration == nil || run.Status != "done" || run.Archived {
				t.Fatalf("metadata did not survive restart: %+v", run)
			}
			return
		}
	}
	t.Fatal("run missing after restart")
}

func TestLibraryArchivesIncompleteAndRestoresWithoutMovingFiles(t *testing.T) {
	root := fixture(t)
	dir := filepath.Join(root, "unfinished")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	info := `{"started_at":"2026-09-20T10:00:00Z","commit":"deadbee","model":{"model":"qwen"}}`
	if err := os.WriteFile(filepath.Join(dir, "run.json"), []byte(info), 0644); err != nil {
		t.Fatal(err)
	}
	srv, _ := New(root, nil)
	find := func(s *Server, path string) RunSummary {
		t.Helper()
		runs, err := s.index(true)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range runs {
			if r.Path == path {
				return r
			}
		}
		t.Fatal("missing", path)
		return RunSummary{}
	}
	if !find(srv, "unfinished").Archived {
		t.Fatal("incomplete run not archived")
	}
	if find(srv, "batch").Archived {
		t.Fatal("completed batch archived")
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/library", strings.NewReader(`{"path":"unfinished","name":"Investigate missing grade","archived":false}`)))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	fresh, _ := New(root, nil)
	run := find(fresh, "unfinished")
	if run.Archived || run.DisplayName != "Investigate missing grade" {
		t.Fatal(run)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "run.json")); string(b) != info {
		t.Fatal("archiving changed artifacts")
	}
	for _, path := range []string{"../outside", "missing"} {
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/library", strings.NewReader(`{"path":"`+path+`","archived":true}`)))
		if rec.Code != 404 {
			t.Fatal(rec.Code)
		}
	}
}

func TestActiveRunIsNotAutoArchived(t *testing.T) {
	root := t.TempDir()
	dir := "active"
	_ = os.Mkdir(filepath.Join(root, dir), 0755)
	_ = writeMetadata(filepath.Join(root, dir), RunMetadata{Name: "Still running", StartedAt: time.Now(), Status: "running"}, "eval-run.json")
	srv, _ := New(root, nil)
	srv.jobs = []*job{{id: "live", dir: dir, status: "running", changed: make(chan struct{})}}
	runs, err := srv.index(true)
	if err != nil || len(runs) != 1 {
		t.Fatal(runs, err)
	}
	if runs[0].Archived || runs[0].JobID != "live" {
		t.Fatal(runs[0])
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/library", strings.NewReader(`{"path":"active","archived":true}`)))
	if rec.Code != 409 {
		t.Fatal(rec.Code)
	}
	restarted, _ := New(root, nil)
	runs, _ = restarted.index(true)
	if !runs[0].Archived || runs[0].Status != "interrupted" {
		t.Fatal("stale running metadata treated as live", runs[0])
	}
}

func TestStartJobRejectsInvalidSettingsBeforeCreatingArtifacts(t *testing.T) {
	r, _ := fakeContainer(t, "")
	srv, _ := New(t.TempDir(), r)
	body, _ := json.Marshal(newEval{Tasks: []string{"easy-01-budget-pair"}, Model: &harness.ModelConfig{Model: "missing endpoint"}})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/jobs", strings.NewReader(string(body))))
	if rec.Code < 400 {
		t.Fatal(rec.Body.String())
	}
	entries, _ := os.ReadDir(srv.root)
	if len(entries) != 0 {
		t.Fatal("invalid settings created run artifacts")
	}
}

func TestFinalArtifactsKeepRunAndPartialBatchVisible(t *testing.T) {
	root := fixture(t)
	for _, name := range []string{"artifact-only", "batch/incomplete/results"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "run.json"), []byte(`{"model":{"model":"qwen"}}`), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "artifact-only", "result.json"), []byte(`{"outcome":"failed"}`), 0644); err != nil {
		t.Fatal(err)
	}
	runs, err := discover(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if (r.Path == "artifact-only" || r.Path == "batch") && r.Archived {
			t.Fatalf("useful artifacts hidden: %+v", r)
		}
	}
}

func TestRunMetadataRecordsBuildSourceBranch(t *testing.T) {
	r, _ := fakeContainer(t, "")
	r.NoBuild = false
	source := t.TempDir()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	for _, args := range [][]string{{"init", "-b", "eval-experiment"}, {"-c", "user.name=Eval Test", "-c", "user.email=eval@example.invalid", "commit", "--allow-empty", "-m", "fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = source
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, out)
		}
	}
	r.BuildContext = source
	meta := r.metadata("Branch experiment", time.Now())
	if meta.Commit == "" || meta.Branch == "" {
		t.Fatal("missing checkout provenance", meta)
	}
	dir := t.TempDir()
	if err := writeMetadata(dir, meta, "eval-run.json"); err != nil {
		t.Fatal(err)
	}
	summary := RunSummary{Path: ".", Tasks: 1}
	enrichRun(dir, &summary)
	if summary.Branch != meta.Branch || summary.Commit != meta.Commit {
		t.Fatal("lost persisted provenance")
	}
	r.NoBuild = true
	r.Commit = "unrelated-commit"
	if meta = r.metadata("", time.Now()); meta.Branch != "" {
		t.Fatal("inferred a branch for an unrelated reused image", meta)
	}
}
