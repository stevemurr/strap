package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/internal/workflow"
	"github.com/stevemurr/strap/lsp"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

// isolation gives each audit and experiment its own copy of the workspace.
// An auditor writes tests and an experimenter builds a measurement harness;
// neither may change the workspace, which only implementors change. The
// auditor used to be told to copy the package itself; now its shell and file
// tools act on a copy the harness makes when the assignment first uses them.
//
// The copy is transparent: paths naming the workspace are rewritten to the
// copy on the way in and back on the way out, so the model keeps using the
// workspace paths it sees everywhere else. A shell command can still name the
// workspace in a form the rewrite misses; the environment recorder's
// workspace snapshot catches such a write, and the state machine flags it.
type isolation struct {
	dir        string   // The workspace, absolute with symlinks resolved.
	spellings  []string // Every form of dir to rewrite, longest first.
	edits      tool.EditMode
	maxTimeout time.Duration
	current    func(identity.ActorID) (work.Work, bool, error) // The actor's sole active assignment.
	live       func(work.ID, work.Revision) bool
	// languages starts a language manager rooted at a copy, or is nil when
	// the session has none. Each copy gets its own: a server sees one tree.
	languages func(dir string) (*lsp.Manager, error)

	mu     sync.Mutex
	copies map[copyKey]*workspaceCopy
	closed bool
}

type copyKey struct {
	work     work.ID
	revision work.Revision
}

type workspaceCopy struct {
	dir       string
	tools     map[string]tool.Tool
	languages *lsp.Manager // nil without language servers
	scratch   *scratchLanguages
}

// remove closes the copy's language servers and deletes the copy.
func (c *workspaceCopy) remove() error {
	var errs []error
	if c.languages != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		errs = append(errs, c.languages.Close(ctx), c.scratch.Close(ctx))
		cancel()
	}
	return errors.Join(append(errs, os.RemoveAll(c.dir))...)
}

func newIsolation(dir string, edits tool.EditMode, maxTimeout time.Duration, current func(identity.ActorID) (work.Work, bool, error), live func(work.ID, work.Revision) bool, languages func(string) (*lsp.Manager, error)) (*isolation, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	spellings := []string{abs}
	if real, err := filepath.EvalSymlinks(abs); err == nil && real != abs {
		spellings = append(spellings, real)
		abs = real
	}
	sort.Slice(spellings, func(i, j int) bool { return len(spellings[i]) > len(spellings[j]) })
	return &isolation{dir: abs, spellings: spellings, edits: edits, maxTimeout: maxTimeout, current: current, live: live, languages: languages, copies: map[copyKey]*workspaceCopy{}}, nil
}

// tools returns the isolated counterparts of templates, which were built on
// the workspace and supply the definitions the model sees.
// tools wraps templates to act on the caller's copy. outside lets writes land
// outside the copy, such as scratch programs in /tmp; without it they are
// refused.
func (iso *isolation) tools(templates []tool.Tool, outside bool) []tool.Tool {
	out := make([]tool.Tool, len(templates))
	for i, t := range templates {
		out[i] = isolatedTool{Tool: t, iso: iso, outside: outside}
	}
	return out
}

// Close removes every copy.
func (iso *isolation) Close(context.Context) error {
	iso.mu.Lock()
	defer iso.mu.Unlock()
	iso.closed = true
	var errs []error
	for k, c := range iso.copies {
		errs = append(errs, c.remove())
		delete(iso.copies, k)
	}
	return errors.Join(errs...)
}

// copyFor returns the copy for w, making it on first use, and removes the
// copies of assignments that have ended.
func (iso *isolation) copyFor(w work.Work) (*workspaceCopy, error) {
	iso.mu.Lock()
	defer iso.mu.Unlock()
	if iso.closed {
		return nil, errors.New("the session is closing")
	}
	for k, c := range iso.copies {
		if !iso.live(k.work, k.revision) {
			// Stopping its language server can take a moment; the caller
			// is waiting on its own copy.
			go func() { _ = c.remove() }()
			delete(iso.copies, k)
		}
	}
	key := copyKey{w.ID, w.AssignedAtRevision}
	if c, ok := iso.copies[key]; ok {
		return c, nil
	}
	dir, err := os.MkdirTemp("", "strap-"+string(w.ID)+"-")
	if err != nil {
		return nil, err
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	if err = copyTree(iso.dir, dir); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("copy the workspace for %s: %w", w.ID, err)
	}
	local, err := localToolsWithChanges(dir, iso.edits, nil, nil)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	c := &workspaceCopy{dir: dir, tools: map[string]tool.Tool{}}
	if iso.languages != nil {
		if c.languages, err = iso.languages(dir); err != nil {
			_ = os.RemoveAll(dir)
			return nil, err
		}
		c.scratch = newScratchLanguages(iso.languages)
		checkWrites(local, c.languages, c.scratch, dir)
		languageTools, err := tool.LSPTools(c.languages)
		if err != nil {
			_ = c.remove()
			return nil, err
		}
		local = append(local, languageTools...)
	}
	for _, t := range local {
		c.tools[t.Definition().Name] = t
		if t.Definition().Name == "shell" {
			// Timed trials run the shell unchecked; the check is not theirs to time.
			c.tools["run_trials"] = trialsTool(t, iso.maxTimeout)
			if c.languages != nil {
				c.tools["shell"] = shellCheck{Tool: t, languages: c.languages, scratch: c.scratch, dir: dir}
			}
		}
	}
	iso.copies[key] = c
	return c, nil
}

// in rewrites workspace paths to the copy's; out rewrites them back.
func (iso *isolation) in(raw []byte, copyDir string) []byte {
	for _, s := range iso.spellings {
		raw = bytes.ReplaceAll(raw, jsonText(s), jsonText(copyDir))
	}
	return raw
}
func (iso *isolation) out(c content.Content, copyDir string) content.Content {
	c = slices.Clone(c)
	for i := range c {
		if c[i].Image == nil {
			c[i].Text = strings.ReplaceAll(c[i].Text, copyDir, iso.dir)
		}
	}
	return c
}

// jsonText is s as it appears inside a JSON string.
func jsonText(s string) []byte {
	b, _ := json.Marshal(s)
	return b[1 : len(b)-1]
}

type isolatedTool struct {
	tool.Tool
	iso     *isolation
	outside bool // writes outside the copy are allowed
}

func (t isolatedTool) InputContract() tool.Contract {
	if typed, ok := t.Tool.(interface{ InputContract() tool.Contract }); ok {
		return typed.InputContract()
	}
	return tool.Contract{}
}
func (t isolatedTool) Validate() error { return tool.ValidateTool(t.Tool) }
func (t isolatedTool) BookkeepingParameters() []string {
	if b, ok := t.Tool.(interface{ BookkeepingParameters() []string }); ok {
		return b.BookkeepingParameters()
	}
	return nil
}

func (t isolatedTool) Call(ctx context.Context, c tool.Call) (tool.Result, error) {
	name := t.Definition().Name
	w, ok, err := t.iso.current(c.Actor)
	if err != nil {
		return tool.Result{}, err
	}
	if !ok {
		return tool.Result{}, fmt.Errorf("%s rejected: nothing ran. Your tools act on your own copy of the workspace, which exists while you hold exactly one active assignment; read get_work for your work_id", name)
	}
	copied, err := t.iso.copyFor(w)
	if err != nil {
		return tool.Result{}, err
	}
	inner, ok := copied.tools[name]
	if !ok {
		return tool.Result{}, fmt.Errorf("%s is not available in a copy of the workspace", name)
	}
	c.Arguments = t.iso.in(c.Arguments, copied.dir)
	if !t.outside {
		if err := outsideWrite(name, c.Arguments, copied.dir); err != nil {
			return tool.Result{}, err
		}
	}
	result, err := inner.Call(ctx, c)
	result.Content = t.iso.out(result.Content, copied.dir)
	result.Captured = t.iso.out(result.Captured, copied.dir)
	if err != nil {
		err = errors.New(strings.ReplaceAll(err.Error(), copied.dir, t.iso.dir))
	}
	return result, err
}

// outsideWrite refuses a file write that would land outside the copy. Only
// the workspace is copied, so a path elsewhere, such as ~/Downloads, names the
// real folder; an auditor or experimenter reads it but never changes it.
func outsideWrite(name string, args json.RawMessage, copyDir string) error {
	if name != "write_file" && name != "edit_file" {
		return nil
	}
	var in struct {
		Input struct {
			Path string `json:"path"`
		} `json:"input"`
	}
	if json.Unmarshal(args, &in) != nil {
		return nil // The tool reports malformed input itself.
	}
	p := tool.ExpandHome(in.Input.Path)
	if !filepath.IsAbs(p) {
		return nil
	}
	if rel, err := filepath.Rel(copyDir, filepath.Clean(p)); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
		return nil
	}
	return fmt.Errorf("%s rejected: nothing was written. %s is outside the workspace; your copy holds only the workspace, so files elsewhere are read-only to you. Write your tests and harness inside the workspace", name, in.Input.Path)
}

// trialsTool runs a command several times through shell and reports each
// trial's wall-clock time.
func trialsTool(shell tool.Tool, maxTimeout time.Duration) tool.Tool {
	return tool.RunTrials(maxTimeout, func(ctx context.Context, c tool.Call, a tool.RunTrialsArgs) (tool.Result, error) {
		args, err := tool.MarshalInput(struct {
			Command   string `json:"command"`
			TimeoutMS *int64 `json:"timeout_ms"`
		}{a.Command, a.TimeoutMS})
		if err != nil {
			return tool.Result{}, err
		}
		var out tool.TrialsResult
		for i := 0; i < a.Trials; i++ {
			if err = ctx.Err(); err != nil {
				return tool.Result{}, err
			}
			started := time.Now()
			r, err := shell.Call(ctx, tool.Call{InvocationID: fmt.Sprintf("%s/%d", c.InvocationID, i+1), Arguments: args, Actor: c.Actor, Sender: c.Sender})
			elapsed := float64(time.Since(started).Microseconds()) / 1000
			var run tool.ShellResult
			if len(r.Captured) > 0 {
				_ = json.Unmarshal([]byte(r.Captured.Text()), &run)
			} else {
				_ = json.Unmarshal([]byte(r.Content.Text()), &run)
			}
			trial := tool.Trial{ElapsedMS: elapsed, ExitCode: run.ExitCode, TimedOut: run.TimedOut}
			if err != nil || run.ExitCode == nil || *run.ExitCode != 0 {
				out.Failed++
			}
			out.Trials = append(out.Trials, trial)
			out.LastOutput = run.Output
			if err != nil && out.LastOutput == "" {
				out.LastOutput = err.Error()
			}
		}
		times := make([]float64, len(out.Trials))
		for i, t := range out.Trials {
			times[i] = t.ElapsedMS
		}
		slices.Sort(times)
		out.MinMS, out.MaxMS = times[0], times[len(times)-1]
		if n := len(times); n%2 == 1 {
			out.MedianMS = times[n/2]
		} else {
			out.MedianMS = (times[n/2-1] + times[n/2]) / 2
		}
		return tool.JSON(out)
	})
}

// copyTree copies the workspace into dst, which exists and is empty. It
// clones files where the file system can (APFS clonefile, reflinks) and
// falls back to copying them.
func copyTree(src, dst string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("cp", "-c", "-R", src+"/.", dst)
	case "linux":
		cmd = exec.Command("cp", "-a", "--reflink=auto", src+"/.", dst)
	}
	if cmd != nil && cmd.Run() == nil {
		return nil
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil || rel == "." {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.Type().IsRegular():
			return copyFile(path, target, info.Mode().Perm())
		}
		return nil // Sockets, devices and pipes are not part of a workspace copy.
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return errors.Join(err, out.Close())
}

// shellMaxTimeout is the local shell's default maximum, which run_trials
// shares for each trial.
const shellMaxTimeout = 5 * time.Minute

// methodFiles reads an experiment's method files from actor's copy of the
// workspace, where the experimenter wrote them.
func (iso *isolation) methodFiles(actor identity.ActorID, paths []string) ([]work.MethodFile, error) {
	w, ok, err := iso.current(actor)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("method files are read from your copy of the workspace, which exists while you hold exactly one active assignment")
	}
	copied, err := iso.copyFor(w)
	if err != nil {
		return nil, err
	}
	var out []work.MethodFile
	for _, p := range paths {
		name := p
		for _, s := range iso.spellings {
			if rel, err := filepath.Rel(s, p); err == nil && filepath.IsAbs(p) && !strings.HasPrefix(rel, "..") {
				name = rel
				break
			}
		}
		if filepath.IsAbs(name) {
			return nil, fmt.Errorf("%w: method file %s is outside the workspace; name files you wrote in it", work.ErrInvalid, p)
		}
		full := filepath.Join(copied.dir, name)
		if rel, err := filepath.Rel(copied.dir, full); err != nil || strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("%w: method file %s is outside the workspace; name files you wrote in it", work.ErrInvalid, p)
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, fmt.Errorf("%w: method file %s: %v", work.ErrInvalid, p, strings.ReplaceAll(err.Error(), copied.dir, iso.dir))
		}
		if len(data) > 32*1024 {
			return nil, fmt.Errorf("%w: method file %s is %d bytes; a method file may be 32 KiB", work.ErrInvalid, p, len(data))
		}
		out = append(out, work.MethodFile{Path: filepath.ToSlash(filepath.Clean(name)), Content: string(data)})
	}
	return out, nil
}

// experimenterSpec is the experimenter's spec, or none without local tools:
// an experiment needs a copy of the workspace to measure in.
func experimenterSpec(cfg Config, p provider.Provider, tools []tool.Tool) agent.Spec {
	if !cfg.LocalTools {
		return agent.Spec{}
	}
	return agent.Spec{Provider: p, Prompt: cfg.Experimenter.Prompt, Tools: tools, ReasoningLimit: uint64(cfg.ReasoningLimit), Concurrent: readTools(tools)}
}

// autoAudit makes the workflow assign audits unless the manager does.
func autoAudit(cfg Config) workflow.Option {
	if cfg.ManualAudits {
		return func(*workflow.Session) {}
	}
	return workflow.WithAutoAudit()
}

// reviewerSpec is the reviewer's spec, or none without local tools: there is
// no workspace to review.
func reviewerSpec(cfg Config, p provider.Provider, tools []tool.Tool) agent.Spec {
	if !cfg.LocalTools {
		return agent.Spec{}
	}
	return agent.Spec{Provider: p, Prompt: cfg.Reviewer.Prompt, Tools: tools, ReasoningLimit: uint64(cfg.ReasoningLimit), Concurrent: readTools(tools)}
}

// readTools names the workspace reads among tools. A batch made only of them
// runs at once: reads share the file lock, and writes wait for them.
func readTools(tools []tool.Tool) []string {
	var reads []string
	for _, t := range tools {
		switch n := t.Definition().Name; {
		case n == "read_file" || n == "read_pdf" || n == "glob" || n == "grep_search" || n == "list_directory" || strings.HasPrefix(n, "lsp_"):
			reads = append(reads, n)
		}
	}
	return reads
}

// isolatedDescription tells the model its tools act on a copy.
const isolatedDescription = " Acts on your own copy of the workspace, made when you first use it for an assignment: nothing you change reaches the real workspace. Files outside the workspace, such as in ~/Downloads, are the real ones and read-only to you."

// auditorIsolatedDescription tells an auditor its tools act on a copy and
// that it may write anywhere else.
const auditorIsolatedDescription = " Acts on your own copy of the workspace, made when you first use it for an assignment: nothing you change reaches the real workspace. Paths outside the workspace, such as /tmp, are the real ones, and you may write there, for example scratch programs."

// describedTool appends a note to a tool's description.
type describedTool struct {
	tool.Tool
	note string
}

func (t describedTool) Definition() provider.ToolDefinition {
	d := t.Tool.Definition()
	d.Description += t.note
	return d
}
func (t describedTool) InputContract() tool.Contract {
	if typed, ok := t.Tool.(interface{ InputContract() tool.Contract }); ok {
		return typed.InputContract()
	}
	return tool.Contract{}
}
func (t describedTool) Validate() error { return tool.ValidateTool(t.Tool) }
func (t describedTool) BookkeepingParameters() []string {
	if b, ok := t.Tool.(interface{ BookkeepingParameters() []string }); ok {
		return b.BookkeepingParameters()
	}
	return nil
}
