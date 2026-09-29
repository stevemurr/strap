package tool

import (
	"context"
	"encoding/json"
	"errors"
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
		w.Write([]byte(`{"results":[{"title":"context package","url":"https://pkg.go.dev/context","content":"Package context defines the Context type."},{"title":"no url","url":"","content":"dropped"},{"title":"Go blog","url":"https://go.dev/blog/context","content":"An introduction."}]}`))
	}))
	defer server.Close()
	backend := &tavilyAPI{key: "test-key", endpoint: server.URL, client: server.Client()}
	hits, err := backend.Search(context.Background(), "golang context", 5, "")
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer test-key" {
		t.Errorf("key not sent as a bearer token: %q", auth)
	}
	if got.Query != "golang context" || got.MaxResults != 5 || got.IncludeRawContent != "" {
		t.Errorf("request did not carry the query and limit alone: %+v", got)
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
	backend := &tavilyAPI{key: secret, endpoint: server.URL, client: server.Client()}
	_, err := backend.Search(context.Background(), "q", 3, "")
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
	backend := &tavilyAPI{key: "k", endpoint: server.URL, client: server.Client()}
	if _, err := backend.Search(context.Background(), "q", 3, ""); err == nil {
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
	if _, ok := w.worker.(*tavilyAPI); !ok {
		t.Fatalf("backend is %T, want the search API", w.worker)
	}
}

// Asked for pages, the backend requests markdown page text and hands it on; a
// page the API could not extract stays a snippet.
func TestTavilyReturnsPageTextWhenAsked(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)
		w.Write([]byte(`{"results":[{"title":"Go 1.26","url":"https://go.dev/doc/go1.26","content":"snippet","raw_content":"# Go 1.26 Release Notes\n\nThe latest Go release."},{"title":"Video","url":"https://video.test","content":"snippet","raw_content":null}]}`))
	}))
	defer server.Close()
	backend := &tavilyAPI{key: "k", endpoint: server.URL, client: server.Client()}
	hits, err := backend.Search(context.Background(), "go release", 2, "markdown")
	if err != nil {
		t.Fatal(err)
	}
	if got["include_raw_content"] != "markdown" {
		t.Errorf("page text not requested as markdown: %v", got)
	}
	if len(hits) != 2 || hits[0].page != "# Go 1.26 Release Notes\n\nThe latest Go release." || hits[1].page != "" || hits[1].Snippet != "snippet" {
		t.Fatalf("hits: %+v", hits)
	}
}

// A search API result carries the start of its page, and the searching agent
// reads the rest through open_url's cursor without a browser. A host search
// asks for visible text and retains nothing, and a negative page size turns
// page text off.
func TestSearchPagesReadOnWithoutABrowser(t *testing.T) {
	page := strings.Repeat("世界 release notes\n", 400)
	var requested []any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)
		requested = append(requested, got["include_raw_content"])
		response, _ := json.Marshal(map[string]any{"results": []map[string]any{
			{"title": "Notes", "url": "https://go.dev/doc/go1.26", "content": "snippet", "raw_content": page},
			{"title": "Video", "url": "https://video.test/watch", "content": "snippet", "raw_content": nil},
		}})
		w.Write(response)
	}))
	defer server.Close()
	w := testWeb(t, WebConfig{TavilyAPIKey: "k", TavilySearchURL: server.URL, SearchPageChars: 1000})
	w.browserErr = errors.New("no browser")
	if !strings.Contains(w.Tools()[0].Definition().Description, "next_cursor") {
		t.Fatal("description does not say how to read on")
	}
	args, _ := MarshalInput(map[string]any{"query": "go release", "max_results": nil})
	r, err := w.Tools()[0].Call(context.Background(), Call{Actor: "a", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	var found WebSearchResult
	if err := json.Unmarshal([]byte(r.Content.Text()), &found); err != nil || len(found.Results) != 2 {
		t.Fatalf("%+v %v", found, err)
	}
	hit, bare := found.Results[0], found.Results[1]
	if requested[0] != "markdown" || hit.Content != string([]rune(page)[:1000]) || !hit.Truncated || hit.NextCursor == "" || hit.Snippet != "snippet" {
		t.Fatalf("page start: %+v", hit)
	}
	if bare.Content != "" || bare.Truncated || bare.NextCursor != "" {
		t.Fatalf("an unextracted page claimed text: %+v", bare)
	}
	rest := openResult(t, w, "a", hit.URL, hit.NextCursor, 50000)
	if hit.Content+rest.Content != page || rest.Truncated || rest.ContentType != "text/markdown" || rest.Title != "Notes" {
		t.Fatalf("read on: %+v", rest)
	}
	if _, err := w.continuePage("b", hit.URL, hit.NextCursor, 200); err == nil {
		t.Fatal("another agent read the searching agent's page")
	}
	cached := len(w.cache)
	host, err := w.SearchWeb(context.Background(), "go release", 2, 1000)
	if err != nil || requested[1] != "text" || host.Results[0].Content != string([]rune(page)[:1000]) || !host.Results[0].Truncated || host.Results[0].NextCursor != "" || len(w.cache) != cached {
		t.Fatalf("host search page text: %v %+v %v", requested, host, err)
	}
	host, err = w.SearchWeb(context.Background(), "go release", 2, 0)
	if err != nil || requested[2] != nil || host.Results[0].Content != "" {
		t.Fatalf("host search asked for page text: %v %+v %v", requested, host, err)
	}
	if _, err := w.SearchWeb(context.Background(), "go release", 2, 100); err == nil {
		t.Fatal("accepted a page size below 200")
	}
	off := testWeb(t, WebConfig{TavilyAPIKey: "k", TavilySearchURL: server.URL, SearchPageChars: -1})
	r, err = off.Tools()[0].Call(context.Background(), Call{Actor: "a", Arguments: args})
	if err != nil || requested[3] != nil || strings.Contains(r.Content.Text(), `"content"`) || strings.Contains(off.Tools()[0].Definition().Description, "next_cursor") {
		t.Fatalf("page text not turned off: %v %v", requested, err)
	}
}
