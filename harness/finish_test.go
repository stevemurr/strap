package harness

import (
	"strings"
	"testing"

	"github.com/stevemurr/strap/prompt"
)

// A request opts into the tester only by asking for adversarial testing in
// so many words.
func TestTesterRequestMatchesExplicitAsks(t *testing.T) {
	for text, want := range map[string]bool{
		"Implement Merge, and run an adversarial tester on it.": true,
		"Please adversarially test the parser":                  true,
		"red-team this change before you finish":                true,
		"have someone try to break it":                          true,
		"Implement Merge in merge.go":                           false,
		"break this function into smaller ones":                 false,
		"add tests for the parser":                              false,
		"don't break the build":                                 false,
	} {
		if got := testerRequest.MatchString(text); got != want {
			t.Errorf("%q: matched %v, want %v", text, got, want)
		}
	}
}

// The tester judges failing tests against the requirements itself unless it
// reports them all, and in both it tests only the contract's exported API.
func TestTesterPromptLeavesJudgmentToTheAgentWhenReportingAll(t *testing.T) {
	judge, all := testerPrompt(false), testerPrompt(true)
	if !strings.Contains(strings.Join(judge.Instructions, " "), "the requirements support its expectation") || strings.Contains(strings.Join(judge.Instructions, " "), "Do not decide") {
		t.Fatal(judge.Instructions)
	}
	if strings.Contains(strings.Join(all.Instructions, " "), "the requirements support its expectation") || !strings.Contains(strings.Join(all.Instructions, " "), "Do not decide") {
		t.Fatal(all.Instructions)
	}
	for _, p := range []prompt.Prompt{judge, all} {
		if !strings.Contains(strings.Join(p.Instructions, " "), "Test only through the exported functions") {
			t.Fatal(p.Instructions)
		}
	}
}
