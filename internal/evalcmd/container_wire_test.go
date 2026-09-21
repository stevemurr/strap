package evalcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/stevemurr/strap/eval"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/internal/evalwire"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolvedContainerConfigStreamsProgressAndKeepsMounts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
			Tools []struct {
				Function struct {
					Strict bool `json:"strict"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "snapshot-model" || len(body.Tools) == 0 {
			t.Error("snapshot model/tools lost", body)
		}
		for _, tool := range body.Tools {
			if !tool.Function.Strict {
				t.Error("strict tool flag lost")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Finished."},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	cfg := harness.DefaultConfig()
	cfg.Model = harness.ModelConfig{Backend: "chatcompletions", Model: "snapshot-model", BaseURL: server.URL, Timeout: time.Minute}
	cfg.LSP = nil
	cfg.Dir = "/old-host/workspace"
	snapshot := evalwire.Config{Version: evalwire.Version, Harness: cfg, Profile: "chosen"}
	b, _ := json.Marshal(snapshot)
	file := filepath.Join(t.TempDir(), "run.json")
	if err := os.WriteFile(file, b, 0600); err != nil {
		t.Fatal(err)
	}
	ladder, _ := filepath.Abs("../../eval/ladder")
	mounts := eval.Mounts{Workspace: t.TempDir(), Results: t.TempDir(), Outbox: t.TempDir(), Problems: ladder}
	var out, logs bytes.Buffer
	args := []string{"-problem", "easy-01-budget-pair", "-run-config", file, "-progress-json", "-quiet", "1ms"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := runMounted(ctx, args, &out, &logs, mounts); err != nil {
		t.Fatal(err, logs.String())
	}
	decoder := json.NewDecoder(&out)
	count := 0
	for {
		var p evalwire.Progress
		err := decoder.Decode(&p)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal("human output corrupted progress stream", err)
		}
		if _, err := p.Decode(eval.Task{ID: "easy-01-budget-pair"}); err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count == 0 {
		t.Fatal("no progress")
	}
	b, _ = os.ReadFile(filepath.Join(mounts.Results, "run.json"))
	var run eval.RunInfo
	if err := json.Unmarshal(b, &run); err != nil {
		t.Fatal(err)
	}
	if run.Profile != "chosen" || run.Model.Model != "snapshot-model" || run.Mounts.Workspace != mounts.Workspace {
		t.Fatal(run)
	}
	b, _ = os.ReadFile(filepath.Join(mounts.Results, "results.jsonl"))
	var result eval.Result
	_ = json.Unmarshal(b, &result)
	if result.Workspace != mounts.Workspace {
		t.Fatal("host snapshot overrode fixed mounts", result)
	}
	if err := runMounted(ctx, append(args, "-model", "other"), io.Discard, io.Discard, mounts); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatal("mixed snapshot and flags", err)
	}
}
