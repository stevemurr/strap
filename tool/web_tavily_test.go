package tool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTavilySearchReturnsRankedHits(t *testing.T) {
	var got tavilyRequest
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Error(err)
		}
		w.Write([]byte(`{"results":[
			{"title":"context package","url":"https://pkg.go.dev/context","content":"Package context defines the Context type."},
			{"title":"no url","url":"","content":"dropped"},
			{"title":"Go blog","url":"https://go.dev/blog/context","content":"An introduction."}]}`))
	}))
	defer server.Close()
	backend := &tavilySearch{key: "test-key", endpoint: server.URL, client: server.Client()}
	hits, err := backend.Search(context.Background(), "golang context", 5)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer test-key" {
		t.Errorf("key not sent as a bearer token: %q", auth)
	}
	if got.Query != "golang context" || got.MaxResults != 5 {
		t.Errorf("request did not carry the query and limit: %+v", got)
	}
	// A result without a URL cannot be opened, so it is not a hit.
	if len(hits) != 2 || hits[0].URL != "https://pkg.go.dev/context" || hits[0].Snippet == "" {
		t.Fatalf("hits: %+v", hits)
	}
}

// The key reaches the model and the trace if it appears in an error, so a
// rejection reports the status and nothing else.
func TestTavilyRejectionDoesNotQuoteTheKey(t *testing.T) {
	const secret = "tvly-secret-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid api key "+secret, http.StatusUnauthorized)
	}))
	defer server.Close()
	backend := &tavilySearch{key: secret, endpoint: server.URL, client: server.Client()}
	_, err := backend.Search(context.Background(), "q", 3)
	if err == nil {
		t.Fatal("a rejected key was accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error quoted the key: %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error hides the cause: %v", err)
	}
}

func TestTavilyEmptyResultsAreAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	backend := &tavilySearch{key: "k", endpoint: server.URL, client: server.Client()}
	if _, err := backend.Search(context.Background(), "q", 3); err == nil {
		t.Fatal("an empty result set was reported as success")
	}
}

// A configured key selects the API backend without a browser present.
func TestAPIKeySelectsTheSearchBackend(t *testing.T) {
	w, err := NewWeb(WebConfig{WKRenderPath: "/missing/wkrender", TavilyAPIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close(context.Background())
	if w.searchErr != nil {
		t.Fatalf("a configured key still reported a missing backend: %v", w.searchErr)
	}
	if _, ok := w.worker.(*tavilySearch); !ok {
		t.Fatalf("backend is %T, want the search API", w.worker)
	}
}
