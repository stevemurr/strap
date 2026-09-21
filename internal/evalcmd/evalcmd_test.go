package evalcmd

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestTierSelection(t *testing.T) {
	for _, tc := range []struct {
		tiers string
		count int
	}{{"", 60}, {"easy", 20}, {" easy, hard,easy ", 40}, {"hard,medium,easy", 60}} {
		s := selection{ladder: "../../eval/ladder", tier: tc.tiers}
		tasks, err := s.load()
		if err != nil || len(tasks) != tc.count {
			t.Fatalf("%q: %d tasks, %v", tc.tiers, len(tasks), err)
		}
		if tc.count == 60 && tasks[0].Tier != "easy" {
			t.Fatal("tier order changed")
		}
	}
	s := selection{ladder: "../../eval/ladder", tier: "easy,medium", tasks: "easy-01-budget-pair,hard-01-rolling-peak"}
	tasks, err := s.load()
	if err != nil || len(tasks) != 1 || tasks[0].ID != "easy-01-budget-pair" {
		t.Fatal(tasks, err)
	}
	for _, tier := range []string{"eazy", "easy,", "easy,,hard", ",", " "} {
		for _, command := range []string{"list", "selfcheck"} {
			err := Main(context.Background(), []string{command, "-tier", tier}, io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "invalid tier") {
				t.Fatalf("%s %q: %v", command, tier, err)
			}
		}
	}
}

func TestNonTerminalDisplay(t *testing.T) {
	for _, mode := range []string{"auto", "plain", "quiet"} {
		enabled, err := useTUI(mode, strings.NewReader(""), new(bytes.Buffer))
		if enabled || err != nil {
			t.Fatal(mode, enabled, err)
		}
	}
	for _, mode := range []string{"tui", "invalid"} {
		if _, err := useTUI(mode, strings.NewReader(""), io.Discard); err == nil {
			t.Fatal("accepted", mode)
		}
	}
}

func TestContainerCLIRejectsLegacyWorkflow(t *testing.T) {
	for _, flag := range []string{"-out", "-scratch", "-parallel", "-ladder", "-task", "-tier"} {
		err := Main(context.Background(), []string{"run", flag, "value"}, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
			t.Fatalf("%s: %v", flag, err)
		}
	}
	for _, args := range [][]string{{"run"}, {"-q"}, {"run", "-problem", ""}} {
		if err := Main(context.Background(), args, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "-problem is required") {
			t.Fatal(args, err)
		}
	}
}

func TestQuietRunReturnsErrors(t *testing.T) {
	for _, args := range [][]string{{"-q"}, {"-ui", "quiet"}} {
		var out, stderr bytes.Buffer
		args = append([]string{"run"}, args...)
		args = append(args, "-problem", "easy-01-budget-pair", "-config", filepath.Join(t.TempDir(), "missing.json"))
		err := Main(context.Background(), args, &out, &stderr)
		if err == nil || !strings.Contains(err.Error(), "missing.json") {
			t.Fatalf("quiet run lost configuration error: %v", err)
		}
		if out.Len() != 0 {
			t.Fatalf("failed run emitted a success summary: %s", out.String())
		}
	}
}
