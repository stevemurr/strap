package evalcmd

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/replay"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/prompt"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
)

const probeUsage = `usage: strap eval probe [-agent ID] [-call N] [-samples N] [-parallel N]
                        [-variants V,V+W,...] [-base-url URL] [-out DIR] TRACE|DIR

Replays a recorded run up to one model call, takes the exact request the
harness built there, and samples the live model on it under each variant,
changing one thing at a time. It reports what each variant's samples did:
called a tool, wrote a tool call out as text, answered in text, or failed.

-agent and -call pick the call (default agent-1, call 1). -base-url points the
recorded model at another server. Variants combine factors with +:

`

// factor is one change a probe variant can make.
type factor struct {
	about     string
	configure func(*harness.Config, roster.Role)
	model     func(*harness.ModelConfig)
	request   func(*provider.Request)
	after     func(provider.Message) []provider.Message
}

func float(v float64) *float64 { return &v }
func integer(v int) *int       { return &v }
func boolean(v bool) *bool     { return &v }

// dropInstruction removes a role's instructions that start with prefix.
func dropInstruction(prefix string) func(*harness.Config, roster.Role) {
	return func(c *harness.Config, role roster.Role) {
		if p := rolePrompt(c, role); p != nil {
			p.Instructions = slices.DeleteFunc(p.Instructions, func(s string) bool { return strings.HasPrefix(s, prefix) })
		}
	}
}

// replaceInstruction swaps a role's instruction that starts with prefix.
func replaceInstruction(prefix, text string) func(*harness.Config, roster.Role) {
	return func(c *harness.Config, role roster.Role) {
		if p := rolePrompt(c, role); p != nil {
			for i, s := range p.Instructions {
				if strings.HasPrefix(s, prefix) {
					p.Instructions[i] = text
				}
			}
		}
	}
}

// auditReference is a candidate auditor method: a reference implementation,
// comparison against it on generated inputs, and tests at every stated limit.
const auditReference = "Independent verification means checks of your own, not rerunning the implementor's tests. Build them in three parts and run all three. 1. Reference: write a deliberately simple implementation from the recorded requirements, and the README where they defer to it; slow is fine, it only has to be obviously correct. 2. Comparison: generate many small inputs, including empty values, duplicates, reorderings of the stated examples, and the smallest and largest allowed values, and check that the submission matches the reference on every one; report the first mismatch as a finding. 3. Limits: test each stated limit exactly at the limit and one past it, and time one input at the largest size the contract names. Cite the results in your summary or findings. Never create or change files in the workspace; only the implementor changes it. To test inside the package, copy it first, for example cp -r /workspace /tmp/audit, then write and run your tests in the copy. Passing the implementor's tests shows their claims are consistent, not that the requirements are met."

// auditVariations leads with the input variations the audit-reference probe
// showed the model follows, then the reference comparison it mostly skipped.
const auditVariations = "Independent verification means checks of your own, not rerunning the implementor's tests. Write tests from the recorded requirements, and the README where they defer to it, that cover every one of these: each stated example in its original order and reordered; empty input, a single element, and duplicates; every stated edge case; each stated limit exactly at the limit and one past it; and one input at the largest size the contract names, timed. Where a simple, obviously correct implementation is easy to write from the requirements, also compare the submission against it on generated inputs. Run the tests and cite the results in your summary or findings. Never create or change files in the workspace; only the implementor changes it. To test inside the package, copy it first, for example cp -r /workspace /tmp/audit, then write and run your tests in the copy. Passing the implementor's tests shows their claims are consistent, not that the requirements are met."

// measureNotRead is a candidate manager instruction against answering
// questions about the code's behavior from reading it.
const measureNotRead = "Reading code shows what it says, not how it behaves. When the user asks how the code performs or behaves, such as whether it is slow, where it spends time, or whether a bug happens and why, assign an experimenter and answer from its delivered conclusion, not from your own reading."

// Candidate wordings for the manager's instruction against answering from
// memory; each replaces the one that starts with recallPrefix.
const (
	recallPrefix    = "You have no web tools, and what you remember"
	recallCutoff    = "Your knowledge comes from training data that ends before today, and you have no web tools. Software releases, versions, APIs, documentation, prices and news may have changed since; treat what you remember about them as possibly out of date. When the user asks about such facts, assign a web_researcher and answer from its delivered brief."
	recallWhen      = "Assign a web_researcher before you answer whenever the answer could have changed since your training or depends on a specific source: software versions and release dates, library and API behavior, documentation, prices, news, or facts about a particular product, project, company or person. Answer from memory only stable general knowledge, such as how a programming language construct works or a mathematical fact."
	recallSpecifics = "Specific facts, such as version numbers, release dates, API names and figures, are what memory gets wrong most often, and you have no web tools. Never state one from memory: have a web_researcher confirm it and answer from its delivered brief."
)

// unknownNameReviewer is a candidate manager instruction for names it cannot
// place, which it cannot look up itself.
const unknownNameReviewer = "A name you don't recognise, such as a function, type, file or project, is most likely in the workspace: send a reviewer to find it instead of asking the user what it is."

// implementDirectly is a candidate manager instruction against surveying the
// workspace with a reviewer before an implementation the request already
// specifies.
const implementDirectly = "When the request already says what to change, such as implementing a named function to a contract in a named file, plan it and assign the implementation directly: the implementor reads the files it needs itself. Send a reviewer first only when the user asks about the files, or when you cannot tell what the phases are without knowing what the files contain."

// surveyLicense is the manager prompt's permission to survey before planning.
const surveyLicense = "When you need to know what the workspace contains before you can plan, or the user asks about files"

// batchCheck is a candidate instruction for roles that change files: issue
// the check with the change instead of in the next model call.
const batchCheck = "Issue the command that checks a change in the same batch as the change: write_file or edit_file, then shell with the build or test that checks it. A batch runs in order, and if the change fails, the calls after it are skipped."

// Candidate auditor instructions against false alarms: the ladder's red
// audit runs (2026-09-26) were all the auditor's own tests being wrong, from
// hand-worked expected values, generators that broke their own assumptions,
// and names that clashed with the implementor's tests.
const (
	contractChecks     = "For inputs beyond the README's worked examples, do not work out the expected result by hand. Check the result against the contract instead: that it has every property the contract requires and, where the contract picks one answer among several, that a brute-force search over the input finds none the contract prefers. Take exact expected values only from the README's worked examples."
	preconditionChecks = "When a test generates inputs, have it check that each one meets the contract's preconditions and every property the test relies on, such as being sorted or free of duplicates, before using it: a generator bug otherwise looks like an implementation defect."
	auditNames         = "Put your checks in a new _test.go file in the code's own package, and start every test function name with TestAudit, so none clashes with the implementor's tests."
)

// Candidate auditor instructions against explaining away a failing test:
// ladder medium-10 (2026-09-26) failed because the auditor's correct test of
// empty targets with an unknown dependency failed, and it deleted the test as
// "unspecified" alongside two tests whose expected values were its own mistake.
const (
	failingTestVerdict = "When one of your own tests fails, decide which is wrong before you change anything: find the requirement or README rule that gives the expected result for that input. If one does, the submission is wrong: keep the test and record the failure as a finding. Change or remove a test only when you can name the rule that shows its expected value is wrong, never just to make a run pass."
	generalRules       = "Each rule in the requirements and the README applies to every input it covers. A statement about one case, such as empty input, adds to the general rules rather than replacing them, so an input that case does not mention must still satisfy them."
)

// Candidate instructions against narrowing a rule to its example while
// writing criteria: in the same medium-10 run the reviewer's proposed_steps
// turned the README's "a dependency names a target that is not in targets"
// into "returns error for unknown target zzz", and the manager copied it into
// the plan the auditor checks.
const (
	reviewerGeneralCriteria = "When you propose acceptance criteria from a document, state each of its rules in general form, as the document words it, such as which inputs are errors. Add the document's examples as criteria of their own, never in place of the rule they illustrate."
	managerGeneralCriteria  = "Write each rule from the request, the README or a brief into acceptance_criteria in its general form, such as which inputs must be rejected, or say that it follows the README. An example of a rule is an extra criterion, never a replacement for the rule."
)

// Candidate wordings for refusing a pass verdict while a test the auditor ran
// failed and has not passed since. They follow the recorded submit_audit of
// ladder medium-10's auditor (agent-4 call 16 of fbrc-all), after it deleted
// the failing test; medium10FailedTests is that case as the rule would list it.
const (
	medium10FailedTests = "TestVerify_Empty_NonEmptyDeps failed in execution:0koy3e1 (builds_verify_test.go:154: expected error when targets empty but deps reference targets not present) and is no longer in your files."
	refusePassList      = "pass refused: a test you ran failed and has not passed since. " + medium10FailedTests
	refusePassAccount   = refusePassList + " For each, run it again until it passes, submit fail with the failure as a finding, or submit pass again with a summary that names the requirement showing the test's expected result was wrong."
	refusePassQuote     = refusePassList + " For each, run it again until it passes, or submit fail with the failure as a finding. To pass without it, your summary must quote the sentence of the requirements or README that makes the test's expected result wrong."
)

// refuseTool answers the recorded response's calls to name with a tool
// error, as the harness renders a refused call.
func refuseTool(name, text string) func(provider.Message) []provider.Message {
	return func(m provider.Message) []provider.Message {
		var out []provider.Message
		for _, c := range m.ToolCalls {
			if c.Name == name {
				out = append(out, provider.Message{Role: "tool", Content: content.Text("Tool error: " + text), ToolCallID: c.ID})
			}
		}
		return out
	}
}

// Candidate auditor lines for limits reached by more than one path: ladder
// medium-20 (fbrc-all, 2026-09-26) failed on "10000000[a]b" because only
// repeated groups were checked against the size limit, and the audit crossed
// the limit only with groups.
const (
	limitPaths      = "Reach each stated limit through every way the result can grow, not only the easiest one: for a size limit, cross it with each kind of element that adds to the size, exactly at the limit and one past it."
	limitPathsOrder = limitPaths + " Vary where the element that crosses the limit sits, such as first, last, or after the rest has already reached the limit."
)

// edgeCombinations is a candidate auditor line for edge cases tested only in
// their simplest form: in fbrc-all and the medium-10 A/B (2026-09-27) audits
// tested "empty" with every argument empty (medium-10), crossed the size limit
// only with groups (medium-20) and tried malformed input only bare (hard-10).
const edgeCombinations = "Test each edge case in combination as well as alone: make one input empty, single or extreme while the others stay ordinary and non-empty; reach each stated limit through every kind of element that adds to it; and put each stated error case inside an otherwise valid input, before and after other elements, not only on its own."

// reduceAndTrace is a candidate auditor line against dismissing a correct
// failing check: in fbrc-all and the medium-10 A/B (2026-09-27) auditors saw
// their own tests catch the bug (hard-09's reference model, medium-10's empty
// targets with deps) and declared the test wrong or the case unspecified
// without evidence.
const reduceAndTrace = "When your own test or reference disagrees with the submission, do not decide which is wrong by assumption. Reduce the failing input to the shortest case that still disagrees, work that case by hand step by step from the requirements and the README, and quote the rule that decides it. If the rule supports your test, the failure is a finding; change your test only when the rule shows its expectation was wrong."

// auditInPackage is a stronger form of auditNames: fbrc-all (2026-09-26)
// auditors wrote scratch package main programs beside the code 31 times in 13
// tasks, then spent turns on package clashes and scratch-module setup.
const auditInPackage = "Write your checks as _test.go files in the code's own directory, declared in the code's package, and run them with go test. Do not write package main programs or new modules to call the code: a package main file beside the code breaks its package, and a separate module has to be wired to import it."

// reviewerLine appends a line to the reviewer's prompt.
func reviewerLine(line string) func(*harness.Config, roster.Role) {
	return func(c *harness.Config, role roster.Role) {
		if role == roster.Reviewer {
			c.Reviewer.Prompt.Instructions = append(c.Reviewer.Prompt.Instructions, line)
		}
	}
}

// auditorLine appends a line to the auditor's prompt.
func auditorLine(line string) func(*harness.Config, roster.Role) {
	return func(c *harness.Config, role roster.Role) {
		if role == roster.Auditor {
			c.Auditor.Prompt.Instructions = append(c.Auditor.Prompt.Instructions, line)
		}
	}
}

// batchCheckExample spells the batch out: two tool calls in one response,
// and why waiting for the edit's result first is unnecessary.
const batchCheckExample = "Make a change and run its check in one response with two tool calls, not two responses: for example edit_file on merge_test.go followed by shell running go test ./... in the same response. You do not need to see the edit's result first: calls in one response run in order, and if the edit fails, the shell call is skipped and you are told why. Only write files with write_file or edit_file, never through shell."

// qwenParallel is Qwen Code's instruction on several tool calls per response,
// verbatim (QwenLM/qwen-code core/prompts.ts).
const qwenParallel = "Call independent tools in parallel; run dependent calls sequentially, using earlier results to supply later arguments."

// everyRoleLine appends a line to every role's prompt.
func everyRoleLine(line string) func(*harness.Config, roster.Role) {
	return func(c *harness.Config, role roster.Role) {
		if p := rolePrompt(c, role); p != nil {
			p.Instructions = append(p.Instructions, line)
		}
	}
}

// fixBeforeRun is a candidate instruction for acting on the language
// server's errors that write results now carry (2026-09-26: after 8 writes
// that reported errors, the next call ran the build twice).
const fixBeforeRun = "When a write_file or edit_file result lists errors from the language server, fix them before you run anything: the build would fail on the same errors."

// fixBeforeRunChecks extends fixBeforeRun to shell results, whose
// diagnostics field lists the errors in files the command changed.
const fixBeforeRunChecks = "When a result lists errors from the language server, after write_file, edit_file or a shell command that changed files, fix them before you run anything: the build would fail on the same errors."

// workerLine appends a line to the prompts of the roles that change files.
func workerLine(line string) func(*harness.Config, roster.Role) {
	return func(c *harness.Config, role roster.Role) {
		if role != roster.Implementor && role != roster.Auditor && role != roster.Experimenter {
			return
		}
		if p := rolePrompt(c, role); p != nil {
			p.Instructions = append(p.Instructions, line)
		}
	}
}

// managerLine appends a line computed at probe time to the manager's prompt.
func managerLine(line func() string) func(*harness.Config, roster.Role) {
	return func(c *harness.Config, role roster.Role) {
		if role == roster.Manager {
			c.Manager.Prompt.Instructions = append(c.Manager.Prompt.Instructions, line())
		}
	}
}

// shortAuditOutput is a candidate auditor instruction for shorter output.
const shortAuditOutput = "Keep your reasoning and text short: decide the next check, run it, and state its result in a line. Do not restate the requirements, the code or earlier results."

var factors = map[string]factor{
	"audit-variations": {about: "auditor must cover reorderings, empty/single/duplicate inputs, stated edge cases and each limit at and past it", configure: replaceInstruction("Independent verification means", auditVariations)},
	"audit-reference":  {about: "auditor verifies against a reference on generated inputs and at every stated limit", configure: replaceInstruction("Independent verification means", auditReference)},
	"recorded":         {about: "the recorded request and model settings"},
	"thinking":         {about: "thinking on", model: func(m *harness.ModelConfig) { m.Generation.EnableThinking = boolean(true) }},
	"no-thinking":      {about: "thinking off", model: func(m *harness.ModelConfig) { m.Generation.EnableThinking = boolean(false) }},
	"qwen-nonthinking": {about: "Qwen's non-thinking sampling: temperature 0.7, top_p 0.8, top_k 20", model: func(m *harness.ModelConfig) {
		m.Generation.Temperature, m.Generation.TopP, m.Generation.TopK = float(0.7), float(0.8), integer(20)
	}},
	"greedy":           {about: "temperature 0", model: func(m *harness.ModelConfig) { m.Generation.Temperature = float(0) }},
	"drop-commentary":  {about: `remove the "Accompany the first tool batch…" instruction`, configure: dropInstruction("Accompany the first tool batch")},
	"drop-schema-note": {about: `remove the "Every tool takes exactly one input object…" instruction`, configure: dropInstruction("Every tool takes exactly one input object")},
	"no-workspace": {about: "remove the workspace listing from wake observations", request: func(r *provider.Request) {
		r.Messages = slices.DeleteFunc(r.Messages, func(m provider.Message) bool {
			return m.Envelope != nil && m.Envelope.Kind == message.Observation && m.Envelope.Workspace != nil && m.Envelope.State == nil
		})
	}},
	"no-self-implement": {about: "remove the manager's permission and procedure for implementing a phase itself", configure: func(c *harness.Config, role roster.Role) {
		if role != roster.Manager {
			return
		}
		p := &c.Manager.Prompt
		p.Instructions = slices.DeleteFunc(p.Instructions, func(s string) bool { return strings.HasPrefix(s, "You implement a phase yourself") })
		for i, s := range p.Instructions {
			p.Instructions[i] = strings.Replace(s, "; you may also implement a phase yourself when it is small and fully specified.", ".", 1)
		}
	}},
	"no-write-tools": {about: "remove write_file, edit_file, shell, submit_work, report_work_progress and reassign_work from the request", request: func(r *provider.Request) {
		drop := map[string]bool{"write_file": true, "edit_file": true, "shell": true, "submit_work": true, "report_work_progress": true, "reassign_work": true}
		r.Tools = slices.DeleteFunc(r.Tools, func(t provider.ToolDefinition) bool { return drop[t.Name] })
	}},
	"no-read-phrase": {about: `remove "Once you have read what you need," from the manager's plan-and-assign instruction`, configure: func(c *harness.Config, role roster.Role) {
		if role != roster.Manager {
			return
		}
		for i, s := range c.Manager.Prompt.Instructions {
			c.Manager.Prompt.Instructions[i] = strings.Replace(s, "Once you have read what you need, turn a request", "Turn a request", 1)
		}
	}},
	"no-read-tools-note": {about: "remove the sentence about what the manager's read tools are for", configure: func(c *harness.Config, role roster.Role) {
		if role != roster.Manager {
			return
		}
		for i, s := range c.Manager.Prompt.Instructions {
			c.Manager.Prompt.Instructions[i] = strings.Replace(s, " Your read tools, read_file, glob, grep_search and LSP, are for planning assignments and checking reported results; you have no shell and cannot write files.", "", 1)
		}
	}},
	"no-manager-reads": {about: "remove the manager's file and LSP tools and its workspace listing", request: func(r *provider.Request) {
		reads := map[string]bool{"read_file": true, "read_pdf": true, "glob": true, "grep_search": true, "list_directory": true}
		r.Tools = slices.DeleteFunc(r.Tools, func(t provider.ToolDefinition) bool { return reads[t.Name] || strings.HasPrefix(t.Name, "lsp_") })
		r.Messages = slices.DeleteFunc(r.Messages, func(m provider.Message) bool {
			return m.Envelope != nil && m.Envelope.Kind == message.Observation && m.Envelope.Workspace != nil && m.Envelope.State == nil
		})
	}},
	"plan-assign-procedure": {about: "add an explicit next-call sequence to the manager prompt: create_plan, create_agent implementor, assign_task implementation, wait_for_input", configure: func(c *harness.Config, role roster.Role) {
		if role != roster.Manager {
			return
		}
		c.Manager.Prompt.Instructions = append(c.Manager.Prompt.Instructions, "When a request needs changes, once you have read enough to plan, your next calls are: create_plan; create_agent with role implementor; assign_task with kind implementation, that agent_id as assignee and the plan's step_ids in scope; wait_for_input. The implementor writes the code; you never write it in a message or reply.")
	}},
	"measure-not-read": {about: "tell the manager that behavior questions are answered from an experimenter's measurements, not its own reading", configure: func(c *harness.Config, role roster.Role) {
		if role == roster.Manager {
			c.Manager.Prompt.Instructions = append(c.Manager.Prompt.Instructions, measureNotRead)
		}
	}},
	"no-recall-off":    {about: "remove the manager's no-recall instruction", configure: dropInstruction(recallPrefix)},
	"date":             {about: "tell the manager today's date", configure: managerLine(func() string { return "Today's date is " + time.Now().Format("2006-01-02") + "." })},
	"cutoff":           {about: "replace no-recall with a knowledge-cutoff framing", configure: replaceInstruction(recallPrefix, recallCutoff)},
	"when-to-search":   {about: "replace no-recall with a list of when to research and when memory is fine", configure: replaceInstruction(recallPrefix, recallWhen)},
	"verify-specifics": {about: "replace no-recall with never stating specifics from memory", configure: replaceInstruction(recallPrefix, recallSpecifics)},
	"no-audit-brief":   {about: "no audit brief: the auditor gets no requirements or changed files with its assignment", configure: func(c *harness.Config, _ roster.Role) { c.AuditBrief = false }},
	"no-audit-runs":    {about: "no recorded runs: the auditor is not told the implementor's build, vet and test results", configure: func(c *harness.Config, _ roster.Role) { c.AuditRuns = false }},
	"short-audit-output": {about: "tell the auditor to keep its reasoning and replies short", configure: func(c *harness.Config, role roster.Role) {
		if role == roster.Auditor {
			c.Auditor.Prompt.Instructions = append(c.Auditor.Prompt.Instructions, shortAuditOutput)
		}
	}},
	"contract-checks":           {about: "auditor checks results beyond the README's examples against the contract (properties, brute force) instead of hand-worked values", configure: auditorLine(contractChecks)},
	"precondition-checks":       {about: "auditor's generated inputs are checked against the contract's preconditions before use", configure: auditorLine(preconditionChecks)},
	"audit-names":               {about: "auditor's tests go in a new file in the code's package, every name prefixed TestAudit (adopted 2026-09-27; adding it again duplicates the line)", configure: auditorLine(auditNames)},
	"audit-names-off":           {about: "remove the adopted audit-names line from the auditor", configure: dropInstruction(auditNames)},
	"fix-before-run-off":        {about: "remove the adopted fix-before-run-checks line from implementor, auditor and experimenter", configure: dropInstruction(fixBeforeRunChecks)},
	"audit-in-package":          {about: "auditor writes checks as _test.go files in the code's package and runs go test; no package main programs or scratch modules", configure: auditorLine(auditInPackage)},
	"failing-test-verdict":      {about: "auditor decides from the rules whether a failing test or the submission is wrong, and keeps a test the rules support", configure: auditorLine(failingTestVerdict)},
	"general-rules":             {about: "auditor applies every general rule to special cases such as empty input", configure: auditorLine(generalRules)},
	"limit-paths":               {about: "auditor crosses each limit through every way the result grows, at the limit and one past", configure: auditorLine(limitPaths)},
	"limit-paths-order":         {about: "limit-paths, and varies where the crossing element sits (first, last, after the limit is reached)", configure: auditorLine(limitPathsOrder)},
	"reduce-and-trace":          {about: "auditor reduces a disagreeing input, traces it by hand from the README and quotes the deciding rule before blaming either side", configure: auditorLine(reduceAndTrace)},
	"edge-combinations":         {about: "auditor tests edge cases in combination: one input empty while others aren't, limits via every path, errors inside valid input", configure: auditorLine(edgeCombinations)},
	"refuse-pass-list":          {about: "follow the recorded submit_audit with a refusal listing the failed test (medium-10)", after: refuseTool("submit_audit", refusePassList)},
	"refuse-pass-account":       {about: "refusal listing the failed test and the three ways out: rerun, fail, or name the requirement (medium-10)", after: refuseTool("submit_audit", refusePassAccount)},
	"refuse-pass-quote":         {about: "refusal listing the failed test; passing without it needs a quoted requirement (medium-10)", after: refuseTool("submit_audit", refusePassQuote)},
	"reviewer-general-criteria": {about: "reviewer's proposed criteria state each document rule in general form, examples only in addition", configure: reviewerLine(reviewerGeneralCriteria)},
	"manager-general-criteria":  {about: "manager's acceptance criteria state each rule in general form or defer to the README, examples only in addition", configure: managerLine(func() string { return managerGeneralCriteria })},
	"fix-before-run-checks":     {about: "fix-before-run for write and shell results (adopted 2026-09-27; adding it again duplicates the line)", configure: workerLine(fixBeforeRunChecks)},
	"fix-before-run":            {about: "tell implementors, auditors and experimenters to fix errors a write reports before running anything", configure: workerLine(fixBeforeRun)},
	"qwen-parallel":             {about: "Qwen Code's line: independent tools in parallel, dependent calls sequentially", configure: everyRoleLine(qwenParallel)},
	"batch-check-example":       {about: "batch-check with an explicit two-call example and why waiting for the edit result is unnecessary", configure: workerLine(batchCheckExample)},
	"batch-check":               {about: "tell implementors, auditors and experimenters to issue a change and the shell check for it in one batch", configure: workerLine(batchCheck)},
	"implement-directly":        {about: "tell the manager to assign an implementation the request already specifies without a reviewer survey first", configure: managerLine(func() string { return implementDirectly })},
	"no-survey-license": {about: "remove \"When you need to know what the workspace contains before you can plan\" from the manager's reviewer routing", configure: func(c *harness.Config, role roster.Role) {
		if role == roster.Manager {
			for i, s := range c.Manager.Prompt.Instructions {
				c.Manager.Prompt.Instructions[i] = strings.Replace(s, surveyLicense, "When the user asks about files", 1)
			}
		}
	}},
	"unknown-name-reviewer": {about: "tell the manager an unrecognised name is most likely in the workspace, so it sends a reviewer instead of asking", configure: managerLine(func() string { return unknownNameReviewer })},
	"no-preserve-thinking":  {about: "preserve_thinking off: the chat template drops thinking from earlier turns", model: func(m *harness.ModelConfig) { m.Generation.PreserveThinking = boolean(false) }},
	"strict-off":            {about: "send tool schemas without strict, so tool-call generation is unconstrained", model: func(m *harness.ModelConfig) { m.LooseTools = true }},
	"short-tool-description": {about: "keep only the first sentence of each tool description", request: func(r *provider.Request) {
		for i, t := range r.Tools {
			if end := strings.Index(t.Description, ". "); end > 0 {
				r.Tools[i].Description = t.Description[:end+1]
			}
		}
	}},
}

func rolePrompt(c *harness.Config, role roster.Role) *prompt.Prompt {
	switch role {
	case roster.Agent:
		return &c.Agent.Prompt
	case roster.Manager:
		return &c.Manager.Prompt
	case roster.Implementor:
		return &c.Implementor.Prompt
	case roster.Auditor:
		return &c.Auditor.Prompt
	case roster.WebResearcher:
		return &c.WebResearcher.Prompt
	case roster.DeepResearcher:
		return &c.DeepResearcher.Prompt
	case roster.Experimenter:
		return &c.Experimenter.Prompt
	case roster.Reviewer:
		return &c.Reviewer.Prompt
	case roster.Debugger:
		return &c.Debugger.Prompt
	}
	return nil
}

// variant combines factors named with +, applied in order.
func variant(name string, role roster.Role) (replay.Variant, error) {
	v := replay.Variant{Name: name}
	var parts []factor
	for _, part := range strings.Split(name, "+") {
		f, ok := factors[strings.TrimSpace(part)]
		if !ok {
			return v, fmt.Errorf("unknown factor %q", part)
		}
		parts = append(parts, f)
	}
	v.Configure = func(c *harness.Config) {
		for _, f := range parts {
			if f.configure != nil {
				f.configure(c, role)
			}
		}
	}
	v.Model = func(m *harness.ModelConfig) {
		for _, f := range parts {
			if f.model != nil {
				f.model(m)
			}
		}
	}
	for _, f := range parts {
		if f.request != nil {
			edit := f.request
			prior := v.Request
			v.Request = func(r *provider.Request) {
				if prior != nil {
					prior(r)
				}
				edit(r)
			}
		}
		if f.after != nil {
			if v.After != nil {
				return v, fmt.Errorf("variant %q follows the recorded call twice", name)
			}
			v.After = f.after
		}
	}
	return v, nil
}

// runVariant applies a variant's factors to a live run's configuration, every
// role's prompt at once. Factors that only edit a recorded request cannot.
func runVariant(name string, cfg *harness.Config) error {
	roles := []roster.Role{roster.Agent, roster.Manager, roster.Debugger, roster.Implementor, roster.Auditor, roster.WebResearcher, roster.DeepResearcher, roster.Experimenter, roster.Reviewer}
	for _, part := range strings.Split(name, "+") {
		part = strings.TrimSpace(part)
		f, ok := factors[part]
		if !ok {
			return fmt.Errorf("unknown factor %q", part)
		}
		if f.request != nil || f.after != nil {
			return fmt.Errorf("factor %q edits a recorded request, so it cannot change a run", part)
		}
		for _, role := range roles {
			if f.configure != nil {
				f.configure(cfg, role)
			}
		}
		if f.model != nil {
			f.model(&cfg.Model)
		}
	}
	return nil
}

func probeCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, probeUsage)
		names := make([]string, 0, len(factors))
		for n := range factors {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(stderr, "  %-24s %s\n", n, factors[n].about)
		}
	}
	agentID := fs.String("agent", "agent-1", "Agent whose model call to probe")
	call := fs.Int("call", 1, "The agent's model call to probe, from 1")
	samples := fs.Int("samples", 10, "Samples per variant")
	parallel := fs.Int("parallel", 4, "Concurrent samples")
	variants := fs.String("variants", "recorded", "Comma-separated variants; + combines factors")
	baseURL := fs.String("base-url", "", "Sample this server instead of the recorded one")
	out := fs.String("out", "", "Write every request and sample as JSON here")
	stale := fs.Bool("allow-stale", false, "Probe a trace the current harness no longer reproduces")
	raw := fs.Bool("raw", false, "Keep each sample's wire request and raw stream frames in -out")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return flag.ErrHelp
	}
	path, err := findTrace(fs.Arg(0))
	if err != nil {
		return err
	}
	rec, err := replay.Open(ctx, path)
	if err != nil {
		return err
	}
	id := identity.ActorID(*agentID)
	role := rec.Role(id)
	opts := replay.ProbeOptions{Agent: id, Call: *call, Samples: *samples, Parallel: *parallel, AllowStale: *stale, Raw: *raw}
	if *baseURL != "" {
		cfg, ok := rec.RoleConfig(role)
		if !ok || cfg.Model == nil {
			return fmt.Errorf("the recording has no model for %s", id)
		}
		m := *cfg.Model
		m.BaseURL = *baseURL
		opts.Model = &m
	}
	for _, name := range strings.Split(*variants, ",") {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		v, err := variant(name, role)
		if err != nil {
			return err
		}
		opts.Variants = append(opts.Variants, v)
	}
	fmt.Fprintf(stdout, "trace %s\nprobing %s (%s) call %d, %d samples per variant\n\n", path, id, role, *call, *samples)
	started := time.Now()
	results, err := replay.Probe(ctx, rec, opts)
	for _, r := range results {
		printVariant(stdout, r)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%d variants in %s\n", len(results), time.Since(started).Round(time.Second))
	if *out != "" {
		if err := os.MkdirAll(*out, 0o755); err != nil {
			return err
		}
		for _, r := range results {
			raw, _ := json.MarshalIndent(r, "", "  ")
			name := strings.NewReplacer("+", "_", "/", "_").Replace(r.Name) + ".json"
			if err := os.WriteFile(filepath.Join(*out, name), raw, 0o644); err != nil {
				return err
			}
		}
		fmt.Fprintf(stdout, "requests and samples in %s\n", *out)
	}
	return nil
}

func printVariant(w io.Writer, r replay.VariantResult) {
	request := "the recorded request"
	if !r.Recorded {
		request = "a changed request"
	}
	var total time.Duration
	for _, s := range r.Samples {
		total += s.Elapsed
	}
	mean := time.Duration(0)
	if len(r.Samples) > 0 {
		mean = total / time.Duration(len(r.Samples))
	}
	fmt.Fprintf(w, "== %s (%s; mean %s)\n", r.Name, request, mean.Round(100*time.Millisecond))
	for _, c := range r.Counts() {
		fmt.Fprintf(w, "  %3d/%d  %s\n", c.Count, len(r.Samples), c.Outcome)
	}
	shown := map[string]bool{}
	for _, s := range r.Samples {
		if shown[s.Outcome] {
			continue
		}
		shown[s.Outcome] = true
		example := s.Response.Content
		if len(s.Response.ToolCalls) > 0 {
			example = string(s.Response.ToolCalls[0].Arguments)
		}
		if s.Err != "" {
			example = s.Err
		}
		fmt.Fprintf(w, "  e.g. %s: %s\n", s.Outcome, clipText(example, 240))
	}
	fmt.Fprintln(w)
}

func clipText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
