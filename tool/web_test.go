package tool

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/internal/agentbrowser"
	"github.com/stevemurr/strap/internal/webkit"
	"github.com/stevemurr/strap/message"
)

type pageFunc func(context.Context, string) (agentbrowser.Page, error)

func (f pageFunc) Read(ctx context.Context, url string) (agentbrowser.Page, error) {
	return f(ctx, url)
}

type searchFunc func(context.Context, string) (webkit.Page, error)

func (f searchFunc) Search(ctx context.Context, url string) (webkit.Page, error) { return f(ctx, url) }
func (searchFunc) Close(context.Context) error                                   { return nil }

func testWeb(t *testing.T, config WebConfig) *Web {
	t.Helper()
	config.WKRenderPath, config.AgentBrowserPath = "/missing/wkrender", "/missing/agent-browser"
	w, err := NewWeb(config)
	if err != nil {
		t.Fatal(err)
	}
	w.searchErr, w.browserErr = nil, nil
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := w.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return w
}

func openResult(t *testing.T, w *Web, actor, url, cursor string, limit int) OpenURLResult {
	t.Helper()
	args, _ := json.Marshal(openArgs{URL: url, Cursor: cursor, MaxChars: &limit})
	r, err := w.Tools()[1].Call(context.Background(), Call{Actor: message.ActorID(actor), Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	var page OpenURLResult
	if err := json.Unmarshal([]byte(r.Content.Text()), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestSearchParsingRankingRedirectsAndFailures(t *testing.T) {
	page := `<div class="result"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa">A &amp; <b>B</b></a><div class="result__snippet">This requires JavaScript and discusses captcha.</div></div>
<a class="result__a" href="https://example.com/a#section">duplicate</a><span class="result__snippet">duplicate snippet</span>
<a class="result__a" href="https://duckduckgo.com/y.js?ad=1">ad</a>
<a class="result__a" href="javascript:alert(1)">bad</a>
<a class="result__a" href="https://docs.example.org/">Docs</a><a class="result__snippet">Documentation</a>`
	hits, err := searchResults(page)
	if err != nil || len(hits) != 2 || hits[0].Title != "A & B" || hits[0].URL != "https://example.com/a" || hits[1].Snippet != "Documentation" {
		t.Fatalf("%+v, %v", hits, err)
	}
	for _, tc := range []struct{ html, want string }{
		{`<div class="no-results__message">No matches</div>`, ""},
		{`<form id="challenge-form"></form>`, "challenge"},
		{`<div class="anomaly-modal">Select images</div>`, "challenge"},
		{`<html><body>Consent required</body></html>`, "unrecognized"},
	} {
		got, err := searchResults(tc.html)
		if tc.want == "" {
			if err != nil || got == nil || len(got) != 0 {
				t.Fatalf("empty result: %+v %v", got, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("want %q, got %v", tc.want, err)
		}
	}
}

func TestSearchContractEncodesQueryAndBoundsResults(t *testing.T) {
	w := testWeb(t, WebConfig{})
	query := `site:example.com a&b "quoted" $(literal)`
	w.worker = searchFunc(func(ctx context.Context, raw string) (webkit.Page, error) {
		u, _ := url.Parse(raw)
		if u.Host != "html.duckduckgo.com" || u.Query().Get("q") != query || u.Query().Get("kl") != "us-en" {
			t.Errorf("bad endpoint %s", raw)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("missing overall deadline")
		}
		return webkit.Page{HTML: `<a class="result__a" href="https://a.test">A</a><a class="result__a" href="https://b.test">B</a>`}, nil
	})
	args, _ := json.Marshal(map[string]any{"query": query, "max_results": 1})
	r, err := w.Tools()[0].Call(context.Background(), Call{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	var found WebSearchResult
	if err := json.Unmarshal([]byte(r.Content.Text()), &found); err != nil || found.Query != query || len(found.Results) != 1 {
		t.Fatalf("%+v %v", found, err)
	}
	for _, raw := range []string{`{"query":" "}`, `{"query":"x","max_results":0}`, `{"query":"x","max_results":11}`, `{"query":"x","script":"bad"}`} {
		if _, err := w.Tools()[0].Call(context.Background(), Call{Arguments: []byte(raw)}); err == nil {
			t.Fatal("accepted", raw)
		}
	}
}

func TestOpenContinuationIsImmutableUnicodeAndActorScoped(t *testing.T) {
	w := testWeb(t, WebConfig{MaxPageChars: 600})
	var reads int
	source := strings.Repeat("世界🌎 hello\n", 10000)
	w.browser = pageFunc(func(_ context.Context, url string) (agentbrowser.Page, error) {
		reads++
		return agentbrowser.Page{URL: url + "/final", Title: "Title", ContentType: "text/html", Content: source, Links: []WebLink{{Text: "docs", URL: "https://docs.example.com"}, {URL: "javascript:bad"}}}, nil
	})
	first := openResult(t, w, "a", "https://example.com", "", 200)
	for _, s := range w.cache {
		if cap(s.text) > 2*w.config.MaxPageChars {
			t.Fatal("snapshot retained oversized backing array")
		}
	}
	if !first.Truncated || !first.DocumentTruncated || first.NextCursor == "" || len(first.Links) != 1 || first.FinalURL != "https://example.com/final" {
		t.Fatalf("%+v", first)
	}
	source = "changed website"
	second := openResult(t, w, "a", "https://example.com", first.NextCursor, 200)
	last := openResult(t, w, "a", "https://example.com", second.NextCursor, 200)
	if reads != 1 || last.Truncated || last.NextCursor != "" || !last.DocumentTruncated || first.Content+second.Content+last.Content != string([]rune(strings.Repeat("世界🌎 hello\n", 100))[:600]) {
		t.Fatal("continuation reloaded, lost Unicode, or hid retention limit")
	}
	for _, tc := range []struct{ actor, url, cursor string }{
		{"b", "https://example.com", first.NextCursor}, {"a", "https://different.test", first.NextCursor}, {"a", "https://example.com", "malformed"},
	} {
		if _, err := w.continuePage(message.ActorID(tc.actor), tc.url, tc.cursor, 200); err == nil {
			t.Fatal("accepted foreign/malformed cursor")
		}
	}
	// Replaying a cursor reads exactly the same slice.
	again := openResult(t, w, "a", "https://example.com", first.NextCursor, 200)
	if again.Content != second.Content {
		t.Fatal("cursor was consumed")
	}
	w.now = func() time.Time { return time.Now().Add(11 * time.Minute) }
	if _, err := w.continuePage("a", "https://example.com", first.NextCursor, 200); err == nil || w.cacheBytes != 0 {
		t.Fatal("expired snapshot retained")
	}
}

func TestSnapshotEvictionAndSourceTruncation(t *testing.T) {
	w := testWeb(t, WebConfig{})
	w.browser = pageFunc(func(_ context.Context, url string) (agentbrowser.Page, error) {
		return agentbrowser.Page{URL: url, Content: strings.Repeat("x", 300), ContentType: "text/plain", Truncated: true}, nil
	})
	a := openResult(t, w, "a", "https://a.test", "", 200)
	w.config.CacheBytes = w.cacheBytes + 10 // fit one snapshot, never two
	b := openResult(t, w, "a", "https://b.test", "", 200)
	if _, err := w.continuePage("a", "https://a.test", a.NextCursor, 200); err == nil {
		t.Fatal("old snapshot not evicted")
	}
	last := openResult(t, w, "a", "https://b.test", b.NextCursor, 200)
	if last.Truncated || !last.DocumentTruncated {
		t.Fatal("backend truncation was lost")
	}
}

func TestOpenQueueDeadlineAndRuntimeClose(t *testing.T) {
	w := testWeb(t, WebConfig{})
	started := make(chan struct{}, 3)
	var active atomic.Int32
	w.browser = pageFunc(func(ctx context.Context, _ string) (agentbrowser.Page, error) {
		active.Add(1)
		defer active.Add(-1)
		started <- struct{}{}
		<-ctx.Done()
		return agentbrowser.Page{}, ctx.Err()
	})
	done := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := w.open(context.Background(), Call{}, openArgs{URL: "https://example.com"})
			done <- err
		}()
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("read did not start")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := w.open(ctx, Call{}, openArgs{URL: "https://queued.test"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if len(started) != 0 {
		t.Fatal("queue limit exceeded")
	}
	cleanup, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := w.Close(cleanup); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if active.Load() != 0 {
		t.Fatal("read leaked")
	}
	if _, err := w.open(context.Background(), Call{}, openArgs{URL: "https://example.com"}); err == nil {
		t.Fatal("opened after close")
	}
}

func TestWebMissingBackendsAndValidation(t *testing.T) {
	w, err := NewWeb(WebConfig{WKRenderPath: "/missing/wkrender", AgentBrowserPath: "/missing/browser"})
	if err != nil {
		t.Fatal("construction must be lazy", err)
	}
	defer w.Close(context.Background())
	for i, args := range []string{`{"query":"test"}`, `{"url":"https://example.com"}`} {
		if _, err := w.Tools()[i].Call(context.Background(), Call{Arguments: []byte(args)}); err == nil || !strings.Contains(err.Error(), "requires") {
			t.Fatal(err)
		}
	}
	for _, url := range []string{"file:///tmp/a", "javascript:alert(1)", "https://u:p@example.com", "https://", "https://example.com\n"} {
		if _, err := webURL(url); err == nil {
			t.Fatal("accepted", url)
		}
	}
	for _, cfg := range []WebConfig{{SearchTimeout: -1}, {CacheTTL: -1}, {MaxPageChars: 1}, {CacheBytes: 1}} {
		if _, err := NewWeb(cfg); err == nil {
			t.Fatal("accepted invalid limits")
		}
	}
}
