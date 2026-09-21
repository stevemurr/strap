package evalcmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/eval"
)

func TestMountedCLIProducesSubmissionThenGrade(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Finished."},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	for _, flags := range [][]string{{"-ui", "plain"}, {"-q"}, {"-ui", "tui", "-q"}, {"-q", "-ui", "tui"}} {
		t.Run(strings.Join(flags, "_"), func(t *testing.T) {
			ladder, err := filepath.Abs("../../eval/ladder")
			if err != nil {
				t.Fatal(err)
			}
			mounts := eval.Mounts{Workspace: t.TempDir(), Results: t.TempDir(), Outbox: t.TempDir(), Problems: ladder}
			config := filepath.Join(t.TempDir(), "models.json")
			if err := os.WriteFile(config, []byte(fmt.Sprintf(`{"default":"test","models":{"test":{"backend":"chatcompletions","model":"test","base_url":%q}}}`, server.URL)), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			args := append([]string{"-problem", "easy-01-budget-pair", "-config", config, "-quiet", "1ms", "-lsp=false"}, flags...)
			var out, logs bytes.Buffer
			if err := runMounted(ctx, args, &out, &logs, mounts); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "1/1 submitted") || strings.Contains(out.String(), "passed") {
				t.Fatal(out.String())
			}
			if strings.Contains(strings.Join(flags, " "), "-q") && logs.Len() != 0 {
				t.Fatal(logs.String())
			}
			for _, file := range []string{"report.md", "report.json"} {
				if _, err := os.Stat(filepath.Join(mounts.Results, file)); err != nil {
					t.Fatal(err)
				}
			}
			before, err := eval.Analyze(ctx, mounts.Results)
			if err != nil || before.Tiers[0].Submitted != 1 || before.Tiers[0].Errored != 0 || strings.Contains(before.Markdown(), "## Failures") {
				t.Fatal(before, err)
			}
			// grade is independent of model config and uses a fresh container workspace.
			mounts.Workspace, mounts.Grading = t.TempDir(), ladder
			out.Reset()
			if err := gradeCmd(ctx, []string{"-q"}, &out, io.Discard, mounts); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "easy-01-budget-pair: failed") {
				t.Fatal(out.String())
			}
		})
	}
}
