package evalweb

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/harness"
)

// A model that replies "Finished." without tool calls submits an unchanged
// stub, so the hidden tests fail and the job still exercises every phase:
// start, run, submit, grade, report, index.
func TestJobRunsGradesAndStreamsProgress(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Finished."},"finish_reason":"stop"}]}`)
	}))
	defer model.Close()
	ladder, err := filepath.Abs("../../eval/ladder")
	if err != nil {
		t.Fatal(err)
	}
	cfg := harness.DefaultConfig()
	cfg.Model = harness.ModelConfig{Backend: "chatcompletions", Model: "test", BaseURL: model.URL, Timeout: time.Minute}
	cfg.Web, cfg.LSP = nil, nil
	root := t.TempDir()
	srv, err := New(root, &Runner{Config: cfg, Ladder: ladder, Profile: "test", Commit: "abc1234", Quiet: time.Millisecond, Idle: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	var info runnerInfo
	get(t, srv, "/api/runner", &info)
	if !info.Enabled || len(info.Tasks) < 20 || info.Tasks[0].Prompt != "" {
		t.Fatalf("%+v", info)
	}
	if rec := post(t, srv, "/api/jobs", `{"tasks":["nope"]}`); rec.Code != http.StatusConflict {
		t.Fatal(rec.Body.String())
	}
	rec := post(t, srv, "/api/jobs", `{"tasks":["easy-01-budget-pair"]}`)
	var snap JobSnapshot
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &snap) != nil || snap.Status != "running" {
		t.Fatal(rec.Body.String())
	}
	if rec := post(t, srv, "/api/jobs", `{"tasks":["easy-02-duplicate-email"]}`); rec.Code != http.StatusConflict {
		t.Fatal("second job should wait for the first", rec.Code)
	}

	// Follow the stream to its end; the server closes it when the job stops.
	live := httptest.NewServer(srv)
	defer live.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", live.URL+"/api/jobs/"+snap.ID+"/events", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal(res.Header)
	}
	kinds := map[string]int{}
	var last JobEvent
	ended := false
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 1<<20), 8<<20)
	event := ""
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: ") && event == "progress":
			var e JobEvent
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e); err != nil {
				t.Fatal(err)
			}
			if e.Seq != last.Seq+1 {
				t.Fatalf("sequence gap: %d after %d", e.Seq, last.Seq)
			}
			last = e
			kinds[e.Kind]++
		case line == "event: end":
			ended = true
		}
		if event == "end" {
			ended = true
			break
		}
	}
	if !ended {
		t.Fatal("stream did not end", scanner.Err())
	}
	for _, k := range []string{"job", "phase", "usage", "result"} {
		if kinds[k] == 0 {
			t.Fatalf("no %s events: %v", k, kinds)
		}
	}
	get(t, srv, "/api/jobs/"+snap.ID, &snap)
	task := snap.Tasks[0]
	if snap.Status != "done" || task.Phase != "finished" || task.Outcome != "failed" || task.ModelCalls == 0 || task.Results == "" {
		t.Fatalf("%+v %+v", snap, task)
	}
	for _, file := range []string{"report.md", "report.json", "results.jsonl", "easy-01-budget-pair/trace.jsonl"} {
		if _, err := os.Stat(filepath.Join(root, task.Results, file)); err != nil {
			t.Fatal(err)
		}
	}
	// The finished attempt appears in the index under the batch group.
	var runs struct{ Runs []RunSummary }
	get(t, srv, "/api/runs", &runs)
	// Both the attempt and the batch it belongs to are listed.
	var member, batch *RunSummary
	for i := range runs.Runs {
		switch {
		case runs.Runs[i].Batch:
			batch = &runs.Runs[i]
		case runs.Runs[i].Group == snap.Name:
			member = &runs.Runs[i]
		}
	}
	if len(runs.Runs) != 2 || member == nil || member.Name != "easy-01-budget-pair" || member.Failed != 1 || batch == nil || batch.Path != snap.Name || batch.Members != 1 {
		t.Fatalf("%+v", runs.Runs)
	}
	// Replaying from the last id yields nothing new but still ends.
	req, _ = http.NewRequestWithContext(ctx, "GET", live.URL+"/api/jobs/"+snap.ID+"/events?after="+fmt.Sprint(last.Seq), nil)
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	body := new(strings.Builder)
	scanner = bufio.NewScanner(res2.Body)
	for scanner.Scan() {
		body.WriteString(scanner.Text() + "\n")
	}
	if strings.Contains(body.String(), "event: progress") || !strings.Contains(body.String(), "event: end") {
		t.Fatal(body.String())
	}
}

func TestJobsDisabledWithoutRunner(t *testing.T) {
	srv, _ := New(t.TempDir(), nil)
	var info runnerInfo
	get(t, srv, "/api/runner", &info)
	if info.Enabled {
		t.Fatal("runner should be disabled")
	}
	if rec := post(t, srv, "/api/jobs", `{"tasks":["easy-01-budget-pair"]}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "disabled") {
		t.Fatal(rec.Body.String())
	}
}

func post(t *testing.T, srv http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec
}
