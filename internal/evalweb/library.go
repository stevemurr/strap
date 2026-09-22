package evalweb

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/stevemurr/strap/harness"
)

// Run metadata is independent of the storage directory and survives server restarts.
type RunMetadata struct {
	Name      string                          `json:"name"`
	StartedAt time.Time                       `json:"started_at"`
	Commit    string                          `json:"commit,omitempty"`
	Branch    string                          `json:"branch,omitempty"`
	Profile   string                          `json:"profile,omitempty"`
	Model     harness.ModelConfig             `json:"model"`
	Roles     map[string]*harness.ModelConfig `json:"roles,omitempty"`
	Status    string                          `json:"status"`
}
type libraryEntry struct {
	Name     string `json:"name,omitempty"`
	Archived *bool  `json:"archived,omitempty"`
}
type newEval struct {
	Tasks        []string             `json:"tasks"`
	Name         string               `json:"name,omitempty"`
	Model        *harness.ModelConfig `json:"model,omitempty"`
	ApplyToRoles bool                 `json:"apply_to_roles,omitempty"`
}

func (r *Runner) configured(request newEval) (*Runner, error) {
	copy := *r
	// The job owns all configuration pointers; future requests cannot mutate it.
	b, err := json.Marshal(r.Config)
	if err != nil {
		return nil, err
	}
	copy.Config = harness.Config{}
	if err = json.Unmarshal(b, &copy.Config); err != nil {
		return nil, err
	}
	if request.Model != nil {
		m := *request.Model
		if strings.TrimSpace(m.Model) == "" || m.Timeout <= 0 {
			return nil, errors.New("model and a positive timeout are required")
		}
		resolved, err := m.Resolve()
		if err != nil {
			return nil, err
		}
		if _, err = resolved.NewProvider(http.DefaultClient); err != nil {
			return nil, err
		}
		copy.Config.Model = resolved
		if request.ApplyToRoles {
			copy.Config.Root.Model = nil
			copy.Config.Implementor.Model = nil
			copy.Config.Auditor.Model = nil
			copy.Config.Researcher.Model = nil
		}
	}
	if len(request.Name) > 160 {
		return nil, errors.New("eval name must be at most 160 characters")
	}
	return &copy, nil
}
func (r *Runner) metadata(name string, now time.Time) RunMetadata {
	m := RunMetadata{Name: name, StartedAt: now, Commit: r.Commit, Profile: r.Profile, Model: r.Config.Model, Status: "running",
		Roles: map[string]*harness.ModelConfig{"root": r.Config.Root.Model, "implementor": r.Config.Implementor.Model, "auditor": r.Config.Auditor.Model, "researcher": r.Config.Researcher.Model}}
	source := r.BuildContext
	if source == "" {
		source = filepath.Dir(filepath.Dir(r.Ladder))
	}
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = source
		b, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	commit := git("rev-parse", "HEAD")
	// A build uses the selected source. Reused images have only the recorded
	// binary commit; do not attach a branch from an unrelated checkout.
	if !r.NoBuild {
		m.Commit = commit
		if m.Commit == "" {
			m.Commit = r.Commit
		}
	}
	if commit != "" && m.Commit != "" && strings.HasPrefix(commit, m.Commit) {
		m.Branch = git("symbolic-ref", "--quiet", "--short", "HEAD")
	}
	return m
}
func writeMetadata(dir string, value any, name string) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".metadata-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}
func hasFinalArtifacts(dir string) bool {
	for _, candidate := range []string{filepath.Join(dir, "final-source"), filepath.Join(filepath.Dir(dir), "outbox", "submission")} {
		if entries, err := os.ReadDir(candidate); err == nil && len(entries) > 0 {
			return true
		}
	}
	for _, name := range []string{"result.json", "report.json"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var result struct {
			Outcome string            `json:"outcome"`
			Tasks   []json.RawMessage `json:"tasks"`
		}
		if json.Unmarshal(b, &result) == nil && (result.Outcome != "" || len(result.Tasks) > 0) {
			return true
		}
	}
	return false
}
func enrichRun(root string, r *RunSummary) {
	dir := filepath.Join(root, filepath.FromSlash(r.Path))
	var meta RunMetadata
	if b, err := os.ReadFile(filepath.Join(dir, "eval-run.json")); err == nil && json.Unmarshal(b, &meta) == nil {
		r.Name = meta.Name
		r.StartedAt = meta.StartedAt
		r.Commit = meta.Commit
		r.Branch = meta.Branch
		r.Profile = meta.Profile
		r.Configuration = &meta.Model
		r.RoleModels = meta.Roles
		r.Model = meta.Model.Model
		r.Backend = meta.Model.Backend
		r.Status = meta.Status
		if r.Status == "running" {
			r.Status = "interrupted"
		} // live jobs are overlaid by Server.index
	}
	if r.DisplayName == "" {
		r.DisplayName = strings.TrimSpace(r.Profile)
		if r.DisplayName == "" {
			r.DisplayName = r.Model
		}
		if r.DisplayName == "" {
			r.DisplayName = "Evaluation"
		}
		if !r.StartedAt.IsZero() {
			r.DisplayName += " · " + r.StartedAt.Format("Jan 2, 15:04")
		}
	}
	if meta.Name != "" {
		r.DisplayName = meta.Name
	}
	r.Archived = r.Tasks == 0 && !hasFinalArtifacts(dir)
	if r.Archived {
		r.ArchiveReason = "No readable results or final artifacts"
	}
	var entry libraryEntry
	if b, err := os.ReadFile(filepath.Join(dir, ".eval-library.json")); err == nil && json.Unmarshal(b, &entry) == nil {
		if entry.Name != "" {
			r.DisplayName = entry.Name
		}
		if entry.Archived != nil {
			r.Archived = *entry.Archived
			if r.Archived {
				r.ArchiveReason = "Archived manually"
			} else {
				r.ArchiveReason = ""
			}
		}
	}
}
func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Path     string `json:"path"`
		Name     string `json:"name"`
		Archived *bool  `json:"archived"`
		Delete   bool   `json:"delete"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&request); err != nil {
		fail(w, 400, err)
		return
	}
	runs, err := s.index(true)
	if err != nil {
		fail(w, 500, err)
		return
	}
	var found *RunSummary
	for i := range runs {
		if runs[i].Path == request.Path {
			found = &runs[i]
			break
		}
	}
	if found == nil {
		fail(w, 404, errors.New("unknown run"))
		return
	}
	if found.Status == "running" {
		fail(w, 409, errors.New("wait for this eval to finish before changing or deleting it"))
		return
	}
	if len(request.Name) > 160 {
		fail(w, 400, errors.New("name must be at most 160 characters"))
		return
	}
	dir := filepath.Join(s.root, filepath.FromSlash(found.Path))
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		fail(w, 400, err)
		return
	}
	root, err := filepath.EvalSymlinks(s.root)
	if err != nil {
		fail(w, 500, err)
		return
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		fail(w, 400, errors.New("run must remain beneath results root"))
		return
	}
	if request.Delete {
		for _, run := range runs {
			if run.Status == "running" && strings.HasPrefix(run.Path, found.Path+"/") {
				fail(w, 409, errors.New("this evaluation contains a running task"))
				return
			}
		}
		s.mu.Lock()
		err = os.RemoveAll(dir)
		s.indexed = time.Time{}
		s.reports = map[string]cachedReport{}
		s.mu.Unlock()
		if err != nil {
			fail(w, 500, fmt.Errorf("delete run: %w", err))
			return
		}
		writeJSON(w, map[string]bool{"deleted": true})
		return
	}
	s.mu.Lock()
	var entry libraryEntry
	if b, err := os.ReadFile(filepath.Join(dir, ".eval-library.json")); err == nil {
		_ = json.Unmarshal(b, &entry)
	}
	if request.Name != "" {
		entry.Name = strings.TrimSpace(request.Name)
	}
	if request.Archived != nil {
		entry.Archived = request.Archived
	}
	err = writeMetadata(dir, entry, ".eval-library.json")
	s.indexed = time.Time{}
	s.reports = map[string]cachedReport{}
	s.mu.Unlock()
	if err != nil {
		fail(w, 500, fmt.Errorf("save run: %w", err))
		return
	}
	writeJSON(w, entry)
}
