package evalcmd

import (
	"slices"
	"strings"
	"testing"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
)

// Factors combine with +, each changing only its own part.
func TestProbeVariantsCombineFactors(t *testing.T) {
	v, err := variant("qwen-nonthinking+drop-commentary+short-tool-description", roster.Manager)
	if err != nil {
		t.Fatal(err)
	}
	cfg := harness.DefaultConfig()
	before := len(cfg.Manager.Prompt.Instructions)
	v.Configure(&cfg)
	if len(cfg.Manager.Prompt.Instructions) != before-1 || len(cfg.Implementor.Prompt.Instructions) == 0 {
		t.Fatalf("manager %d → %d instructions", before, len(cfg.Manager.Prompt.Instructions))
	}
	m := harness.ModelConfig{}
	v.Model(&m)
	if *m.Generation.Temperature != 0.7 || *m.Generation.TopP != 0.8 || m.Generation.EnableThinking != nil {
		t.Fatalf("generation %+v", m.Generation)
	}
	r := provider.Request{Tools: []provider.ToolDefinition{{Name: "send_message", Description: "Pass a message. It ends your turn."}}}
	v.Request(&r)
	if r.Tools[0].Description != "Pass a message." {
		t.Fatal(r.Tools[0].Description)
	}
	if _, err := variant("recorded+wizardry", roster.Manager); err == nil || !strings.Contains(err.Error(), "wizardry") {
		t.Fatal(err)
	}
}

// A run variant changes every role's prompt once; request edits are refused.
func TestRunVariantChangesEveryRole(t *testing.T) {
	cfg := harness.DefaultConfig()
	manager, auditor := len(cfg.Manager.Prompt.Instructions), len(cfg.Auditor.Prompt.Instructions)
	if err := runVariant("fix-before-run-checks", &cfg); err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct {
		name         string
		instructions []string
	}{{"implementor", cfg.Implementor.Prompt.Instructions}, {"auditor", cfg.Auditor.Prompt.Instructions}, {"experimenter", cfg.Experimenter.Prompt.Instructions}} {
		if p.instructions[len(p.instructions)-1] != fixBeforeRunChecks {
			t.Fatalf("%s's last instruction is %q", p.name, p.instructions[len(p.instructions)-1])
		}
	}
	if len(cfg.Manager.Prompt.Instructions) != manager || len(cfg.Auditor.Prompt.Instructions) != auditor+1 {
		t.Fatalf("manager %d → %d, auditor %d → %d", manager, len(cfg.Manager.Prompt.Instructions), auditor, len(cfg.Auditor.Prompt.Instructions))
	}
	if err := runVariant("short-tool-description", &cfg); err == nil || !strings.Contains(err.Error(), "recorded request") {
		t.Fatal(err)
	}
}

// The medium-10 A/B arm: the reviewer gets its criteria line on top of the
// workers' fix-before-run line; a refusal factor cannot change a run.
func TestRunVariantReviewerCriteriaArm(t *testing.T) {
	cfg := harness.DefaultConfig()
	manager := len(cfg.Manager.Prompt.Instructions)
	if err := runVariant("fix-before-run-checks+reviewer-general-criteria", &cfg); err != nil {
		t.Fatal(err)
	}
	if r := cfg.Reviewer.Prompt.Instructions; r[len(r)-1] != reviewerGeneralCriteria {
		t.Fatalf("reviewer's last instruction is %q", r[len(r)-1])
	}
	if a := cfg.Auditor.Prompt.Instructions; a[len(a)-1] != fixBeforeRunChecks {
		t.Fatalf("auditor's last instruction is %q", a[len(a)-1])
	}
	if len(cfg.Manager.Prompt.Instructions) != manager {
		t.Fatal("the manager's prompt changed")
	}
	if err := runVariant("refuse-pass-list", &cfg); err == nil {
		t.Fatal("a refusal factor changed a run")
	}
}

// The adopted lines are in every file-changing role's default prompt, and the
// -off factors remove them, as a probe of a trace recorded before 2026-09-27
// needs.
func TestAdoptedLinesAndOffFactors(t *testing.T) {
	cfg := harness.DefaultConfig()
	has := func(p []string, line string) bool { return slices.Contains(p, line) }
	for _, p := range [][]string{cfg.Implementor.Prompt.Instructions, cfg.Auditor.Prompt.Instructions, cfg.Experimenter.Prompt.Instructions} {
		if !has(p, fixBeforeRunChecks) {
			t.Fatal("fix-before-run-checks is not in a file-changing role's prompt")
		}
	}
	if !has(cfg.Auditor.Prompt.Instructions, auditNames) || has(cfg.Implementor.Prompt.Instructions, auditNames) {
		t.Fatal("audit-names belongs to the auditor alone")
	}
	if err := runVariant("fix-before-run-off+audit-names-off", &cfg); err != nil {
		t.Fatal(err)
	}
	for _, p := range [][]string{cfg.Implementor.Prompt.Instructions, cfg.Auditor.Prompt.Instructions, cfg.Experimenter.Prompt.Instructions} {
		if has(p, fixBeforeRunChecks) || has(p, auditNames) {
			t.Fatal("an -off factor left its line in place")
		}
	}
}
