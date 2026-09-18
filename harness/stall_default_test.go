package harness_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/provider"
)

// The watchdog was configured in DefaultConfig and lost by every caller that
// built a ModelConfig from parts, so the guard existed and never ran. The
// default belongs where nothing can skip it: on the way to the provider.
func TestStallBudgetsSurviveAConfigBuiltFromParts(t *testing.T) {
	for _, c := range []struct {
		name  string
		model harness.ModelConfig
		first time.Duration
		idle  time.Duration
	}{
		{"assembled by hand, as the eval does", harness.ModelConfig{Backend: "vllm", BaseURL: "http://model.test", Model: "m", Timeout: time.Minute}, harness.DefaultStallFirstChunk, harness.DefaultStallIdle},
		{"decoded from a catalog profile", decodeProfile(t, `{"backend":"vllm","base_url":"http://model.test","model":"m","timeout_ns":60000000000}`), harness.DefaultStallFirstChunk, harness.DefaultStallIdle},
		{"explicitly chosen", harness.ModelConfig{Backend: "vllm", BaseURL: "http://model.test", Model: "m", Timeout: time.Minute, StallFirstChunk: time.Second, StallIdle: 2 * time.Second}, time.Second, 2 * time.Second},
		{"explicitly disabled", harness.ModelConfig{Backend: "vllm", BaseURL: "http://model.test", Model: "m", Timeout: time.Minute, StallFirstChunk: -1, StallIdle: -1}, -1, -1},
	} {
		t.Run(c.name, func(t *testing.T) {
			resolved, err := c.model.Resolve()
			if err != nil {
				t.Fatal(err)
			}
			if resolved.StallFirstChunk != c.first || resolved.StallIdle != c.idle {
				t.Fatalf("budgets became %s/%s, want %s/%s", resolved.StallFirstChunk, resolved.StallIdle, c.first, c.idle)
			}
		})
	}
}

func decodeProfile(t *testing.T, raw string) harness.ModelConfig {
	t.Helper()
	var m harness.ModelConfig
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// End to end through the provider a session actually builds: a server that
// goes quiet must end the call rather than hang on it.
func TestDefaultedBudgetsReachTheProvider(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		<-release // The server holds the stream open and says nothing more.
	}))
	defer server.Close()
	defer close(release)
	model := harness.ModelConfig{Backend: "vllm", BaseURL: server.URL, Model: "m", Timeout: time.Minute, StallIdle: 200 * time.Millisecond}
	p, err := model.NewProvider(server.Client())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := p.Submit(t.Context(), provider.Request{}, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a silent stream was accepted")
		}
		t.Logf("ended with: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("the watchdog never fired")
	}
}
