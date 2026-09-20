package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/message"
)

// Explicit opt-in: requires macOS/wkrender, agent-browser, Chrome, and
// network access. Ordinary tests never launch a browser or query a search engine.
// STRAP_LIVE_WEB=1 go test -race ./tool -run TestLiveWeb -count=1 -v
func TestLiveWeb(t *testing.T) {
	if os.Getenv("STRAP_LIVE_WEB") != "1" {
		t.Skip("set STRAP_LIVE_WEB=1 to test real web backends")
	}
	w, err := NewWeb(WebConfig{WKRenderPath: os.Getenv("STRAP_WKRENDER"), AgentBrowserPath: os.Getenv("STRAP_AGENT_BROWSER"), BrowserExecutablePath: os.Getenv("STRAP_BROWSER_EXECUTABLE"), MaxPageChars: 1000})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := w.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	var reads atomic.Int32
	slowStarted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			out.Header().Set("Content-Type", "text/html")
			fmt.Fprint(out, "<html><body>Loading...")
			out.(http.Flusher).Flush()
			slowStarted <- struct{}{}
			<-r.Context().Done()
			return
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(out, r, "/page", http.StatusFound)
			return
		}
		if r.URL.Path != "/page" {
			http.NotFound(out, r)
			return
		}
		reads.Add(1)
		out.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(out, `<html><head><title>Rendered fixture</title></head><body><main>Loading...</main><script>setTimeout(() => {document.querySelector('main').innerHTML = '<h1>Ready</h1><pre><code>const answer = 42;</code></pre><a href="/next">Next page</a><p>%s</p>';}, 250)</script></body></html>`, strings.Repeat("Unicode 世界🌎. ", 300))
	}))
	defer server.Close()
	t.Run("render_redirect_links_and_continuation", func(t *testing.T) {
		first := openResult(t, w, "reader", server.URL+"/redirect", "", 200)
		if first.Title != "Rendered fixture" || first.FinalURL != server.URL+"/page" || !strings.Contains(first.Content, "Ready") || !strings.Contains(first.Content, "const answer = 42") || !first.Truncated || !first.DocumentTruncated {
			t.Fatalf("unexpected rendered page: %+v", first)
		}
		foundLink := false
		for _, link := range first.Links {
			foundLink = foundLink || link.URL == server.URL+"/next"
		}
		if !foundLink {
			t.Fatal("link destination lost", first.Links)
		}
		before := reads.Load()
		second := openResult(t, w, "reader", server.URL+"/redirect", first.NextCursor, 200)
		if second.Content == "" || reads.Load() != before {
			t.Fatal("continuation reloaded the site")
		}
		t.Logf("rendered title=%q, final URL=%s, continuation=%d chars", first.Title, first.FinalURL, len([]rune(second.Content)))
	})
	t.Run("concurrent_agents", func(t *testing.T) {
		type outcome struct {
			page OpenURLResult
			url  string
			err  error
		}
		results := make(chan outcome, 2)
		for _, actor := range []string{"a", "b"} {
			go func(actor string) {
				url := server.URL + "/page?actor=" + actor
				args, _ := MarshalInput(openArgs{URL: url})
				r, err := w.Tools()[1].Call(context.Background(), Call{Actor: message.ActorID(actor), Arguments: args})
				var page OpenURLResult
				if err == nil {
					err = json.Unmarshal([]byte(r.Content.Text()), &page)
				}
				results <- outcome{page, url, err}
			}(actor)
		}
		for range 2 {
			r := <-results
			if r.err != nil || r.page.FinalURL != r.url {
				t.Fatalf("browser state crossed agents: %+v", r)
			}
		}
	})
	t.Run("cancel_navigation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := w.open(ctx, Call{}, openArgs{URL: server.URL + "/slow"}); done <- err }()
		select {
		case <-slowStarted:
		case err := <-done:
			t.Fatalf("navigation failed before reaching fixture: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("navigation did not begin")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "close browser session") {
				t.Fatalf("cancellation or cleanup failed: %v", err)
			}
		case <-time.After(8 * time.Second):
			t.Fatal("cancelled browser did not settle")
		}
	})
	t.Run("search", func(t *testing.T) {
		for range 2 {
			start := time.Now()
			r, err := w.Tools()[0].Call(context.Background(), Call{Arguments: []byte(`{"input":{"query":"Go context package documentation","max_results":3}}`)})
			if err != nil {
				t.Fatal(err)
			}
			var result WebSearchResult
			if err := json.Unmarshal([]byte(r.Content.Text()), &result); err != nil || len(result.Results) == 0 || len(result.Results) > 3 {
				t.Fatalf("%+v %v", result, err)
			}
			t.Logf("%d search results in %s; first=%s", len(result.Results), time.Since(start).Round(time.Millisecond), result.Results[0].URL)
		}
	})
}
