package eval_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stevemurr/strap/eval"
)

func gradeWorkspace(t *testing.T, files map[string]string) eval.Grade {
	t.Helper()
	dir := t.TempDir()
	files["go.mod"] = "module probe\n\ngo 1.24\n"
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g, err := eval.RunHiddenTests(context.Background(), eval.Task{TestTimeout: "30s"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

const (
	answer     = "package probe\n\nfunc Answer() int { return 42 }\n"
	hiddenPass = "package probe\n\nimport \"testing\"\n\nfunc TestHiddenAnswer(t *testing.T) {\n\tif Answer() != 42 {\n\t\tt.Fatal(Answer())\n\t}\n}\n"
)

func TestHiddenTestsPassDespiteAnExtraPackageWithoutTests(t *testing.T) {
	// An agent's leftover scratch program: its "[no test files]" line used to
	// fail an otherwise passing grade.
	g := gradeWorkspace(t, map[string]string{
		"probe.go": answer, "probe_hidden_test.go": hiddenPass,
		"cmd/scratch/main.go": "package main\n\nimport \"probe\"\n\nfunc main() { _ = probe.Answer() }\n",
	})
	if !g.Passed || !g.Compiled || !strings.Contains(g.Output, "ok") || !strings.Contains(g.Output, "no test files") || strings.Contains(g.Output, `"Action"`) {
		t.Fatalf("%+v", g)
	}
}

func TestHiddenTestsMustRunAndPass(t *testing.T) {
	for name, c := range map[string]struct {
		files    map[string]string
		compiled bool
	}{
		"failing hidden test": {map[string]string{"probe.go": "package probe\n\nfunc Answer() int { return 7 }\n", "probe_hidden_test.go": hiddenPass}, true},
		"no hidden test ran":  {map[string]string{"probe.go": answer, "probe_test.go": "package probe\n\nimport \"testing\"\n\nfunc TestOwn(t *testing.T) {}\n"}, true},
		"build failure":       {map[string]string{"probe.go": "package probe\n\nfunc Answer() int { return }\n", "probe_hidden_test.go": hiddenPass}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if g := gradeWorkspace(t, c.files); g.Passed || g.Compiled != c.compiled {
				t.Fatalf("%+v", g)
			}
		})
	}
}
