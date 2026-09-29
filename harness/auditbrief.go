package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/internal/workflow"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

// The audit brief hands an auditor, with its assignment, what it otherwise
// spent the start of every audit gathering: the requirements, the
// submission's summary and the files the implementation changed. Across the
// ladder's audits (2026-09-25), 36% of audit time went by before the auditor
// wrote its first test, on get_work, get_plan, reading those files and
// building.

// auditRunsInstruction joins the auditor's prompt when run lists are on. The
// auditors' traces show them rebuilding and re-vetting before every first
// test: two sequential calls repeating runs the implementor already made.
const auditRunsInstruction = "Your audit assignment's context lists the implementor's final build, vet and test runs as the harness recorded them, with their exit codes. Do not run those commands again; spend your checks on tests of the requirements."

// auditBriefInstruction joins the auditor's prompt when briefs are on.
const auditBriefInstruction = "Your audit assignment's context hands you the requirements, the submission's summary and the current contents of the files the implementation changed. Start from it: read other files only when the requirements refer to them, and go straight to writing and running your checks."

const (
	briefFileLimit  = 16 * 1024
	briefTotalLimit = 40 * 1024
)

// changeLog records, for each implementation, the workspace files its
// implementor and repairers changed.
type changeLog struct {
	mu    sync.Mutex
	paths map[work.ID]map[string]bool
	runs  map[work.ID][]recordedRun // In order; a repeated command keeps its latest run.
}

type recordedRun struct {
	command string
	exit    *int
}

// note attributes a call's workspace changes to the actor's active
// implementation, through a repair to the implementation it repairs.
func (s *Session) noteChanges(actor identity.ActorID, paths []string) {
	if len(paths) == 0 || s.workflow == nil {
		return
	}
	w, ok, err := s.workflow.Store.AdmitExecution(actor)
	if err != nil || !ok {
		return
	}
	id := w.ID
	switch w.Kind {
	case work.Repair:
		id = w.ParentID
	case work.Implementation:
	default:
		return
	}
	s.changes.mu.Lock()
	defer s.changes.mu.Unlock()
	if s.changes.paths == nil {
		s.changes.paths = map[work.ID]map[string]bool{}
	}
	if s.changes.paths[id] == nil {
		s.changes.paths[id] = map[string]bool{}
	}
	for _, p := range paths {
		s.changes.paths[id][p] = true
	}
}

type briefFile struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
	Missing bool   `json:"missing,omitempty"`
}

// briefReader reads the changed files for a brief. It is an environment
// tool, recorded like read_file, so a replay serves the recorded contents
// instead of reading a workspace that is not there.
func briefReader(dir string) tool.Tool {
	type args struct {
		Paths []string `json:"paths"`
	}
	params, _ := tool.NewParameters[args]()
	return tool.Func[args]{Spec: tool.Definition[args]{Name: "audit_brief_files", Description: "Host read of the files a submission changed, for its audit brief.", Parameters: params}, Invoke: func(_ context.Context, _ tool.Call, a args) (tool.Result, error) {
		var out []briefFile
		for _, p := range a.Paths {
			data, err := os.ReadFile(filepath.Join(dir, p))
			if err != nil {
				out = append(out, briefFile{Path: p, Missing: true})
				continue
			}
			body := string(data)
			if len(body) > briefFileLimit {
				body = body[:briefFileLimit] + "\n… (truncated; read the rest with read_file)"
			}
			out = append(out, briefFile{Path: p, Content: body})
		}
		return tool.JSON(out)
	}}
}

// readBriefFiles reads paths through the recorded brief reader, under an
// invocation named for the audit so a replay finds it.
func (s *Session) readBriefFiles(original work.Work, submission work.SubmissionID, paths []string) ([]briefFile, error) {
	args, err := tool.MarshalInput(struct {
		Paths []string `json:"paths"`
	}{paths})
	if err != nil {
		return nil, err
	}
	r, err := s.briefFiles.Call(context.Background(), tool.Call{InvocationID: "audit-brief/" + string(original.ID) + "/" + string(submission), Arguments: args, Actor: original.Owner})
	if err != nil {
		return nil, err
	}
	var files []briefFile
	if err := json.Unmarshal([]byte(r.Content.Text()), &files); err != nil {
		return nil, err
	}
	return files, nil
}

// auditBriefs installs the audit context builder: the brief, the recorded
// runs, or both, as configured.
func auditBriefs(s *Session, cfg Config, deps Dependencies) workflow.Option {
	if !cfg.AuditBrief && !cfg.AuditRuns {
		return func(*workflow.Session) {}
	}
	if cfg.AuditBrief {
		reader := briefReader(cfg.Dir)
		if deps.Environment != nil {
			reader = deps.Environment(reader)
		}
		s.briefFiles = s.recordEnvironment(reader)
	}
	return workflow.WithAuditBrief(func(original work.Work, submission work.SubmissionID) string {
		var parts []string
		if cfg.AuditBrief {
			parts = append(parts, s.auditBrief(original, submission))
		}
		if cfg.AuditRuns {
			if runs := s.auditRuns(original); runs != "" {
				parts = append(parts, runs)
			}
		}
		return strings.Join(parts, "\n")
	})
}

// maxAuditRuns bounds the runs listed for one audit.
const maxAuditRuns = 8

// noteRun records an implementor's shell run against its implementation,
// from the recorded environment result.
func (s *Session) noteRun(actor identity.ActorID, name string, args json.RawMessage, captured string) {
	if name != "shell" || s.workflow == nil {
		return
	}
	var in struct {
		Input struct {
			Command string `json:"command"`
		} `json:"input"`
	}
	var out struct {
		ExitCode *int `json:"exit_code"`
	}
	if json.Unmarshal(args, &in) != nil || in.Input.Command == "" || json.Unmarshal([]byte(captured), &out) != nil {
		return
	}
	w, ok, err := s.workflow.Store.AdmitExecution(actor)
	if err != nil || !ok {
		return
	}
	id := w.ID
	switch w.Kind {
	case work.Repair:
		id = w.ParentID
	case work.Implementation:
	default:
		return
	}
	s.changes.mu.Lock()
	defer s.changes.mu.Unlock()
	if s.changes.runs == nil {
		s.changes.runs = map[work.ID][]recordedRun{}
	}
	runs := slices.DeleteFunc(s.changes.runs[id], func(r recordedRun) bool { return r.command == in.Input.Command })
	s.changes.runs[id] = append(runs, recordedRun{command: in.Input.Command, exit: out.ExitCode})
}

// auditRuns lists the implementor's latest runs for original.
func (s *Session) auditRuns(original work.Work) string {
	s.changes.mu.Lock()
	runs := slices.Clone(s.changes.runs[original.ID])
	s.changes.mu.Unlock()
	if len(runs) == 0 {
		return ""
	}
	if len(runs) > maxAuditRuns {
		runs = runs[len(runs)-maxAuditRuns:]
	}
	var b strings.Builder
	b.WriteString("The implementor's final runs, as the harness recorded them (not its claims):\n")
	for _, r := range runs {
		exit := "was stopped"
		if r.exit != nil {
			exit = fmt.Sprintf("exited %d", *r.exit)
		}
		fmt.Fprintf(&b, "- `%s` %s\n", r.command, exit)
	}
	b.WriteString("Do not run these again; write and run your own checks of the requirements.\n")
	return b.String()
}

// auditBrief builds the brief for an audit of original's submission.
func (s *Session) auditBrief(original work.Work, submission work.SubmissionID) string {
	store := s.workflow.Store
	var b strings.Builder
	b.WriteString("Handed to you with this audit: the requirements, the submission and the files it changed, as they are now.\n\nRequirements:\n")
	if original.Scope != nil {
		if plan, err := store.GetPlan(original.Owner, original.Scope.PlanID); err == nil {
			for _, step := range plan.Steps {
				if !slices.Contains(original.Scope.StepIDs, step.ID) {
					continue
				}
				fmt.Fprintf(&b, "- %s\n", step.Title)
				for _, c := range step.AcceptanceCriteria {
					fmt.Fprintf(&b, "  - %s\n", c)
				}
			}
		}
	}
	fmt.Fprintf(&b, "- Task: %s\n", original.Task)
	if original.Context != "" {
		fmt.Fprintf(&b, "- Context: %s\n", original.Context)
	}
	if sub, err := store.GetSubmission(original.Owner, submission); err == nil {
		fmt.Fprintf(&b, "\nSubmission summary: %s\n", sub.Summary)
	}
	s.changes.mu.Lock()
	var paths []string
	for p := range s.changes.paths[original.ID] {
		paths = append(paths, p)
	}
	s.changes.mu.Unlock()
	slices.Sort(paths)
	if len(paths) == 0 {
		b.WriteString("\nNo changed workspace files were recorded; read the files the submission names.\n")
		return b.String()
	}
	files, err := s.readBriefFiles(original, submission, paths)
	if err != nil {
		fmt.Fprintf(&b, "\nThe changed files could not be read (%v); read them with read_file: %s\n", err, strings.Join(paths, ", "))
		return b.String()
	}
	b.WriteString("\nChanged files:\n")
	used := 0
	for _, f := range files {
		switch {
		case f.Missing:
			fmt.Fprintf(&b, "\n%s: deleted or unreadable\n", f.Path)
			continue
		case used+len(f.Content) > briefTotalLimit:
			fmt.Fprintf(&b, "\n%s: not included, the brief is full; read it with read_file\n", f.Path)
			continue
		}
		used += len(f.Content)
		fmt.Fprintf(&b, "\n--- %s ---\n%s\n", f.Path, f.Content)
	}
	return b.String()
}
