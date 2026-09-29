package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/prompt"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

// The adversarial tester writes tests to break a solo agent's change, in a
// throwaway copy of the workspace, and reports the ones that fail. The harness
// runs each reported command itself and passes on only the failures it saw:
// the auditors' tests caught real bugs (ladder hard-09, medium-20) but the
// auditors explained them away, so the tester reports evidence, not a verdict.
func testerPrompt(reportAll bool) prompt.Prompt {
	// Judging whether the requirements support a failing test let a tester
	// dismiss the very bug it had caught, citing a narrower README clause
	// (medium-10, 2026-09-28), as the team's auditors had. Reporting every
	// failure leaves that judgment to the agent that wrote the code.
	report := "Run your tests. As soon as one fails and the requirements support its expectation, call report_failure with its file, a command that runs just that test, and the requirement it checks, quoted, before you look for more. When a failure comes from a mistake in your own test, fix the test instead of reporting it."
	if reportAll {
		report = "Run your tests. As soon as one fails, call report_failure with its file, a command that runs just that test, and the requirement it checks, quoted, before you look for more. Do not decide whether a failing expectation is what the requirements really mean; the agent that wrote the code weighs each report against them. Fix a failing test yourself only when the test itself is broken, such as a typo or an arithmetic slip in its expected value."
	}
	return prompt.Prompt{Role: "You are an adversarial tester. Another agent has just changed code to carry out the request below. Your job is to find where the change fails the request's requirements, by writing and running tests. You work in a private copy of the workspace; nothing you change reaches the real one.", Instructions: []string{
		"Read the request, then the README and any document the request names, then the changed files. Take the requirements from the request and those documents, not from the code or its comments.",
		// Tests that named an error variable only the agent's code defined
		// did not compile against the reference solution (medium-20).
		"Write tests the way a strict reviewer would: each stated example, in order and reordered; empty input, a single element and duplicates; each stated edge case; each limit exactly at and one past it, through every kind of input that can reach it; each error case both alone and embedded in otherwise valid input; each stated output for invalid input; and the largest stated size, asserting its time bound. Test only through the exported functions and types the request and its documents describe, never names the implementation added, so the tests check the contract rather than this code. Put them in a new test file beside the code, in the project's usual test form.",
		report,
		"Do not change the code under test. When you have tried what the requirements state, reply with one line saying what you covered.",
	}}
}

// DefaultTesterBudget is a tester run's wall clock unless Config.TesterBudget
// sets one. A run that found the medium-20 limit bug ran out of 6 minutes
// before it could report it (2026-09-28); a call writing tests takes about 20s.
const DefaultTesterBudget = 20 * time.Minute

// TesterBudgetOrDefault is the wall clock one tester run may take.
func (c Config) TesterBudgetOrDefault() time.Duration {
	if c.TesterBudget > 0 {
		return c.TesterBudget
	}
	return DefaultTesterBudget
}

// testerNote tells an agent whose request asked for adversarial testing that
// the harness does it, so it neither attempts it nor says it cannot.
const testerNote = "This request asks for adversarial testing: before your final reply goes out, the harness runs an independent tester against your change and shows you any test of it that fails. Finish and check the work as usual."

const (
	testerCalls    = 150      // Model calls for one run.
	testerFailures = 3        // Failures passed on to the agent.
	testerBound    = 3 << 10  // Bytes of a test file or output passed on.
	testerKept     = 64 << 10 // Bytes of a test file the record keeps.
)

type tester struct {
	provider  provider.Provider
	dir       string
	edits     tool.EditMode
	limit     uint64
	budget    time.Duration // Wall clock for one run.
	reportAll bool          // Report every failing test, leaving the judgment to the agent.
	publish   func(conversation.Event) error
	recorded  func(message.ActorID) (conversation.TesterEvent, bool) // Serves recorded runs; see Dependencies.TesterRuns.
}

// run tests the change the agent made for request and returns the failures the
// harness reproduced. It records the run as a TesterEvent.
func (t *tester) run(ctx context.Context, agent message.ActorID, request string, changed []string) []conversation.TestFailure {
	if t.recorded != nil {
		e, ok := t.recorded(agent)
		if !ok {
			e = conversation.TesterEvent{Agent: agent, Request: request, Changed: changed, Error: "no recorded tester run left to serve"}
		}
		_ = t.publish(e)
		return firstFailures(e.Failures)
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, t.budget)
	defer cancel()
	e := conversation.TesterEvent{Agent: agent, Request: request, Changed: changed}
	defer func() {
		e.Duration = time.Since(start)
		_ = t.publish(e)
	}()
	copyDir, err := os.MkdirTemp("", "strap-tester-")
	if err != nil {
		e.Error = err.Error()
		return nil
	}
	defer os.RemoveAll(copyDir)
	if real, err := filepath.EvalSymlinks(copyDir); err == nil {
		copyDir = real
	}
	if err := copyTree(t.dir, copyDir); err != nil {
		e.Error = fmt.Sprintf("copy the workspace: %v", err)
		return nil
	}
	local, err := localToolsWithChanges(copyDir, t.edits, nil, nil)
	if err != nil {
		e.Error = err.Error()
		return nil
	}
	tools := map[string]tool.Tool{}
	var shell tool.Tool
	for _, l := range local {
		tools[l.Definition().Name] = l
		if l.Definition().Name == "shell" {
			shell = l
		}
	}
	var mu sync.Mutex
	tools["report_failure"] = tool.ReportFailure(func(ctx context.Context, c tool.Call, a tool.ReportFailureArgs) (tool.Result, error) {
		// The harness runs the command itself; only a failure it sees counts.
		res, err := shell.Call(ctx, tool.Call{InvocationID: c.InvocationID + "-check", Arguments: shellArgs(a.Command), Actor: c.Actor})
		if err != nil {
			return tool.Result{}, err
		}
		if shellStatus(res) == "" {
			return tool.Text("That command passed when the harness ran it, so nothing was reported. Report a command that fails."), nil
		}
		path := a.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(copyDir, path)
		}
		source, _ := os.ReadFile(path)
		mu.Lock()
		// The record keeps the whole test, for judging the tester later; the
		// agent's notice shows it bounded.
		e.Failures = append(e.Failures, conversation.TestFailure{Path: a.Path, Command: a.Command, Requirement: a.Requirement, Test: string(source[:min(len(source), testerKept)]), Output: bounded(shellOutput(res))})
		mu.Unlock()
		return tool.Text("Reported: the command fails."), nil
	})
	var defs []provider.ToolDefinition
	for _, name := range []string{"read_file", "glob", "grep_search", "list_directory", "write_file", "edit_file", "shell", "report_failure"} {
		if d, ok := tools[name]; ok {
			defs = append(defs, d.Definition())
		}
	}
	rel := make([]string, len(changed))
	for i, p := range changed {
		if r, err := filepath.Rel(t.dir, p); err == nil && !strings.HasPrefix(r, "..") {
			p = r
		}
		rel[i] = p
	}
	system, err := testerPrompt(t.reportAll).Render()
	if err != nil {
		e.Error = err.Error()
		return nil
	}
	history := []provider.Message{
		{Role: "system", Content: content.Text(system)},
		{Role: "user", Content: content.Text(fmt.Sprintf("Request:\n%s\n\nChanged files: %s", request, strings.Join(rel, ", ")))},
	}
	defer func() { e.Transcript = history }()
	warned := false
	var steps []conversation.TesterStep
	defer func() { e.Steps = steps }()
	for e.Calls < testerCalls && ctx.Err() == nil {
		if !warned && time.Since(start) > t.budget*3/4 {
			warned = true
			history = append(history, provider.Message{Role: "user", Content: content.Text("Your time is nearly up. Report each failure you have confirmed now with report_failure, then reply.")})
		}
		e.Calls++
		step := conversation.TesterStep{At: time.Since(start)}
		resp, err := t.submit(ctx, history, defs)
		step.Model = time.Since(start) - step.At
		if err != nil {
			steps = append(steps, step)
			e.Error = err.Error()
			break
		}
		history = append(history, provider.Message{Role: "assistant", Content: content.Text(resp.Content), Reasoning: resp.Reasoning, ToolCalls: resp.ToolCalls})
		if len(resp.ToolCalls) == 0 {
			steps = append(steps, step)
			break
		}
		toolStart := time.Now()
		for i, c := range resp.ToolCalls {
			step.Calls = append(step.Calls, c.Name)
			out := "unknown tool " + c.Name
			if tl := tools[c.Name]; tl != nil {
				res, err := tl.Call(ctx, tool.Call{InvocationID: fmt.Sprintf("tester-%d-%d", e.Calls, i), Arguments: c.Arguments, Actor: agent})
				if err != nil {
					out = "error: " + err.Error()
				} else {
					out = res.Content.Text()
				}
			}
			history = append(history, provider.Message{Role: "tool", ToolCallID: c.ID, Content: content.Text(out)})
		}
		step.Tools = time.Since(toolStart)
		steps = append(steps, step)
	}
	return firstFailures(e.Failures)
}

// firstFailures bounds the failures passed on to the agent.
func firstFailures(f []conversation.TestFailure) []conversation.TestFailure {
	return f[:min(len(f), testerFailures)]
}

// submit makes one model call, cutting it off once its reasoning passes the
// session's limit.
func (t *tester) submit(ctx context.Context, history []provider.Message, defs []provider.ToolDefinition) (provider.Response, error) {
	reasoning := uint64(0)
	return t.provider.Submit(ctx, provider.Request{Agent: "tester", Messages: history, Tools: defs}, provider.ObserverFunc(func(d provider.Delta) error {
		if d.Channel == provider.ChannelReasoning {
			reasoning += uint64(len(d.Text))
			if t.limit > 0 && reasoning > t.limit {
				return errors.New("tester reasoning limit reached")
			}
		}
		return nil
	}))
}

func shellArgs(command string) []byte {
	return []byte(fmt.Sprintf(`{"input":{"command":%q,"timeout_ms":null}}`, command))
}

// shellOutput is a shell result's output text.
func shellOutput(r tool.Result) string {
	var out struct {
		tool.ShellResult
		Result *tool.ShellResult `json:"result"`
	}
	for _, part := range r.Content {
		if json.Unmarshal([]byte(part.Text), &out) == nil {
			if out.Result != nil {
				return out.Result.Output
			}
			return out.Output
		}
	}
	return r.Content.Text()
}

func bounded(s string) string {
	if len(s) > testerBound {
		return s[:testerBound/2] + "\n…\n" + s[len(s)-testerBound/2:]
	}
	return s
}

// testerNotice tells the agent what the tester's reproduced failures show.
func testerNotice(failures []conversation.TestFailure) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Before you reply: an independent tester wrote tests from the request, and the harness ran them against your change: %d fail. Fix the code; if a test contradicts the request or its documents, say which one and why in your reply.", len(failures))
	for i, f := range failures {
		fmt.Fprintf(&b, "\n\n%d. %s\nChecks: %s\nCommand: %s\nTest file:\n%s\nOutput:\n%s", i+1, f.Path, f.Requirement, f.Command, bounded(f.Test), f.Output)
	}
	return b.String()
}
