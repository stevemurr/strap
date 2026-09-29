package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/lsp"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/tool"
)

// finishFacts is what the harness observed of a single agent's work since its
// exchange began: the files it changed with the write tools and how the
// commands it ran after its latest change ended. The finish check holds the
// agent's reply once when those facts say the change is unchecked or failing.
// It checks facts, not opinions: an auditor asked to judge the work passed
// bugs its own tests had caught (ladder medium-19, hard-09, 2026-09-28).
type finishFacts struct {
	dir       string
	languages *lsp.Manager
	scratch   *scratchLanguages

	tester *tester // A solo session's adversarial tester.
	always bool    // Run the tester for every request, not only those that ask.

	mu        sync.Mutex
	request   string   // What the user asked for this exchange.
	requested bool     // This exchange runs the tester.
	tested    bool     // The tester ran this exchange.
	held      int      // Replies the facts held this exchange.
	changed   []string // Files written this exchange, in first-write order.
	ran       bool     // A command ran after the latest write.
	failed    string   // The latest such command, when it did not exit 0.
	status    string
}

// modelRetries is how many more times a model call that failed with a
// retryable error is made; see provider.WithRetries.
const modelRetries = 3

// finishCheckFiles bounds the changed files the check reports on.
const finishCheckFiles = 5

func newFinishFacts(dir string, languages *lsp.Manager, scratch *scratchLanguages) *finishFacts {
	// Written paths are resolved, so the workspace must be too for the check
	// to name files relative to it.
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return &finishFacts{dir: dir, languages: languages, scratch: scratch}
}

// testerRequest matches a user message that asks for adversarial testing,
// which opts that request into the tester.
var testerRequest = regexp.MustCompile(`(?i)\badversarial(ly)?\b|\bred[\s-]?team(ing|ed)?\b|\btry (to|and) break (it|this|them|that|my)\b`)

// reset starts a new exchange's facts; inputs are the messages it answers.
func (f *finishFacts) reset(inputs []message.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changed, f.ran, f.failed, f.status, f.tested, f.held = nil, false, "", "", false, 0
	var request []string
	for _, m := range inputs {
		if m.From == message.User && strings.TrimSpace(m.Content) != "" {
			request = append(request, m.Content)
		}
	}
	if len(request) > 0 {
		f.request = strings.Join(request, "\n\n")
		f.requested = f.tester != nil && (f.always || testerRequest.MatchString(f.request))
	}
}

// testerRequested reports whether this exchange runs the tester.
func (f *finishFacts) testerRequested() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requested
}

func (f *finishFacts) wrote(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ran, f.failed, f.status = false, "", ""
	for _, p := range f.changed {
		if p == path {
			return
		}
	}
	f.changed = append(f.changed, path)
}

func (f *finishFacts) command(command, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.changed) == 0 {
		return
	}
	f.ran, f.failed, f.status = true, "", ""
	if status != "" {
		f.failed, f.status = command, status
	}
}

// check returns the notice that holds the reply, or "" to let it through.
// Once the facts are clean, a session with the tester runs it once per
// exchange over a changed code file.
func (f *finishFacts) check(ctx context.Context, self message.ActorID) string {
	f.mu.Lock()
	changed, ran, failed, status := append([]string(nil), f.changed...), f.ran, f.failed, f.status
	f.mu.Unlock()
	if notice := f.facts(ctx, changed, ran, failed, status); notice != "" {
		// The facts hold a reply once, and once more after the tester's
		// failures send the agent back to change the code.
		f.mu.Lock()
		defer f.mu.Unlock()
		if limit := 1 + btoi(f.tested); f.held >= limit {
			return ""
		}
		f.held++
		return notice
	}
	f.mu.Lock()
	tested, request, requested := f.tested, f.request, f.requested
	f.tested = f.tested || requested
	f.mu.Unlock()
	if tested || !requested || !f.code(changed) {
		return ""
	}
	if failures := f.tester.run(ctx, self, request, changed); len(failures) > 0 {
		return testerNotice(failures)
	}
	return ""
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// code reports whether a changed file is one a language server covers, or,
// without language servers, whether anything changed.
func (f *finishFacts) code(changed []string) bool {
	if f.languages == nil {
		return len(changed) > 0
	}
	for _, p := range changed {
		if f.languages.Handles(p) {
			return true
		}
	}
	return false
}

func (f *finishFacts) facts(ctx context.Context, changed []string, ran bool, failed, status string) string {
	if len(changed) == 0 {
		return ""
	}
	var notes []string
	if f.languages != nil {
		seen := map[string]bool{}
		for _, path := range changed[:min(len(changed), finishCheckFiles)] {
			if !f.languages.Handles(path) {
				continue
			}
			report := checkPackage(ctx, f.languages, f.scratch, path)
			if strings.HasPrefix(report, "The language server reports") && !seen[report] {
				seen[report] = true
				notes = append(notes, report)
			}
		}
	}
	files := f.names(changed)
	switch {
	case !ran:
		notes = append(notes, fmt.Sprintf("You changed %s and have run nothing since. Build it and run its tests now, or say in your reply why they cannot run.", files))
	case failed != "":
		notes = append(notes, fmt.Sprintf("After your last change to %s, `%s` %s. Fix what failed, or say in your reply why it still fails.", files, clipCommand(failed), status))
	}
	if len(notes) == 0 {
		return ""
	}
	return "Before you reply: " + strings.Join(notes, " ")
}

func (f *finishFacts) names(paths []string) string {
	var out []string
	for _, p := range paths[:min(len(paths), finishCheckFiles)] {
		if rel, err := filepath.Rel(f.dir, p); err == nil && !strings.HasPrefix(rel, "..") {
			p = rel
		}
		out = append(out, p)
	}
	if len(paths) > finishCheckFiles {
		out = append(out, fmt.Sprintf("%d more", len(paths)-finishCheckFiles))
	}
	return strings.Join(out, ", ")
}

func clipCommand(c string) string {
	c = strings.Join(strings.Fields(c), " ")
	if len(c) > 120 {
		c = c[:120] + "…"
	}
	return c
}

// factTool records, for the finish check, the writes and commands that
// succeed as tool calls.
type factTool struct {
	tool.Tool
	facts *finishFacts
}

func (t factTool) InputContract() tool.Contract {
	if typed, ok := t.Tool.(interface{ InputContract() tool.Contract }); ok {
		return typed.InputContract()
	}
	return tool.Contract{}
}
func (t factTool) Validate() error { return tool.ValidateTool(t.Tool) }
func (t factTool) BookkeepingParameters() []string {
	if b, ok := t.Tool.(interface{ BookkeepingParameters() []string }); ok {
		return b.BookkeepingParameters()
	}
	return nil
}

func (t factTool) Call(ctx context.Context, c tool.Call) (tool.Result, error) {
	result, err := t.Tool.Call(ctx, c)
	if err != nil {
		return result, err
	}
	var in struct {
		Input struct {
			Path    string `json:"path"`
			Command string `json:"command"`
		} `json:"input"`
	}
	_ = json.Unmarshal(c.Arguments, &in)
	switch t.Definition().Name {
	case "write_file", "edit_file":
		if in.Input.Path == "" {
			return result, nil
		}
		path := tool.ExpandHome(in.Input.Path)
		if !filepath.IsAbs(path) {
			path = filepath.Join(t.facts.dir, path)
		}
		if real, err := filepath.EvalSymlinks(path); err == nil {
			path = real
		}
		t.facts.wrote(path)
	case "shell":
		t.facts.command(in.Input.Command, shellStatus(result))
	}
	return result, nil
}

// shellStatus describes how a shell call ended, or "" for exit status 0. A
// worker's result wraps the shell's own under "result".
func shellStatus(r tool.Result) string {
	var out struct {
		tool.ShellResult
		Result *tool.ShellResult `json:"result"`
	}
	decoded := false
	for _, part := range r.Content {
		if json.Unmarshal([]byte(part.Text), &out) == nil {
			decoded = true
			break
		}
	}
	if !decoded {
		return "" // An unknown ending holds nothing.
	}
	s := out.ShellResult
	if out.Result != nil {
		s = *out.Result
	}
	switch {
	case s.TimedOut:
		return "timed out"
	case s.ExitCode == nil:
		return "ended without an exit status"
	case *s.ExitCode != 0:
		return fmt.Sprintf("exited with status %d", *s.ExitCode)
	}
	return ""
}

// updateTodos publishes a solo agent's todo list for the user to follow.
func (s *Session) updateTodos(_ context.Context, c tool.Call, a tool.UpdateTodosArgs) (tool.Result, error) {
	todos := make([]conversation.Todo, len(a.Todos))
	counts := map[string]int{}
	for i, t := range a.Todos {
		todos[i] = conversation.Todo{Content: t.Content, Status: t.Status}
		counts[t.Status]++
	}
	if err := s.publish(conversation.TodosEvent{Agent: c.Actor, Todos: todos}); err != nil {
		return tool.Result{}, err
	}
	return tool.Text(fmt.Sprintf("Todo list updated: %d completed, %d in progress, %d pending.", counts["completed"], counts["in_progress"], counts["pending"])), nil
}
