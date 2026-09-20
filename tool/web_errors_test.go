package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stevemurr/strap/internal/agentbrowser"
	"github.com/stevemurr/strap/internal/webkit"
)

func TestSearchFailuresAndDefaultLimit(t *testing.T) {
	backendErr := errors.New("worker failed")
	for _, tc := range []struct {
		name, html, want string
		backendErr       error
		cancel           bool
	}{
		{name: "backend", backendErr: backendErr, want: "worker failed"},
		{name: "oversized", html: strings.Repeat("x", (5<<20)+1), want: "exceeded 5 MiB"},
		{name: "challenge", html: `<form id="challenge-form"></form>`, want: "challenge"},
		{name: "cancelled result", html: `<div class="no-results"></div>`, cancel: true, want: "context canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := testWeb(t, WebConfig{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w.worker = searchFunc(func(context.Context, string) (webkit.Page, error) {
				if tc.cancel {
					cancel()
				}
				return webkit.Page{HTML: tc.html}, tc.backendErr
			})
			_, err := w.Tools()[0].Call(ctx, Call{Arguments: []byte(`{"input":{"query":"test","max_results":null}}`)})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
			if tc.backendErr != nil && !errors.Is(err, tc.backendErr) {
				t.Fatal("lost backend error", err)
			}
		})
	}
	w := testWeb(t, WebConfig{})
	w.worker = searchFunc(func(context.Context, string) (webkit.Page, error) {
		var html strings.Builder
		for _, letter := range "abcdefghij" {
			html.WriteString(`<a class="result__a" href="https://` + string(letter) + `.test">Title</a>`)
		}
		return webkit.Page{HTML: html.String()}, nil
	})
	r, err := w.Tools()[0].Call(context.Background(), Call{Arguments: []byte(`{"input":{"query":"  test  ","max_results":null}}`)})
	var got WebSearchResult
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(r.Content.Text()), &got); err != nil || got.Query != "test" || len(got.Results) != 8 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := w.search(context.Background(), Call{}, searchArgs{Query: strings.Repeat("x", 8193)}); err == nil {
		t.Fatal("accepted oversized query")
	}
	if got := searchDestination("https://bad%zz"); got != "" {
		t.Fatal(got)
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.search(context.Background(), Call{}, searchArgs{Query: "test"}); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatal(err)
	}
}

func TestOpenRejectsInvalidRequestsAndPageMetadata(t *testing.T) {
	for _, tc := range []struct{ name, url, final, contentType, want string }{
		{"invalid request", "file:///tmp/page", "", "", "absolute HTTP"},
		{"long request", "https://example.com/" + strings.Repeat("x", 8192), "", "", "URL exceeds"},
		{"invalid final", "https://example.com", "file:///tmp/page", "text/plain", "invalid final URL"},
		{"long final", "https://example.com", "https://example.com/" + strings.Repeat("x", 8192), "text/plain", "metadata limits"},
		{"long type", "https://example.com", "https://example.com", strings.Repeat("x", 257), "metadata limits"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := testWeb(t, WebConfig{})
			w.browser = pageFunc(func(context.Context, string) (agentbrowser.Page, error) {
				return agentbrowser.Page{URL: tc.final, ContentType: tc.contentType}, nil
			})
			_, err := w.Tools()[1].Call(context.Background(), Call{Arguments: mustWebJSON(t, map[string]any{"url": tc.url, "cursor": nil, "max_chars": nil})})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
	for _, raw := range []string{`{"input":{"url":"https://example.com","max_chars":199,"cursor":null}}`, `{"input":{"url":"https://example.com","max_chars":50001,"cursor":null}}`, `{"input":{"url":"https://example.com","cursor":"","max_chars":null}}`, `{"input":{"url":"https://example.com","script":"bad","cursor":null,"max_chars":null}}`} {
		w := testWeb(t, WebConfig{})
		if _, err := w.Tools()[1].Call(context.Background(), Call{Arguments: []byte(raw)}); err == nil {
			t.Fatal("accepted", raw)
		}
	}
}

func mustWebJSON(t *testing.T, value any) []byte {
	t.Helper()
	out, err := MarshalInput(value)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestOpenBoundsLinksAndCacheCapacity(t *testing.T) {
	w := testWeb(t, WebConfig{})
	w.browser = pageFunc(func(_ context.Context, url string) (agentbrowser.Page, error) {
		return agentbrowser.Page{URL: url, Title: strings.Repeat("界", 501), Content: strings.Repeat("a", 300), Links: []WebLink{
			{Text: strings.Repeat("界", 201), URL: "https://a.test"},
			{URL: "https://b.test/" + strings.Repeat("x", 8192)},
			{URL: "https://c.test/" + strings.Repeat("x", 8000)},
			{URL: "https://d.test/" + strings.Repeat("x", 8000)},
		}}, nil
	})
	page := openResult(t, w, "a", "https://example.com", "", 200)
	if len([]rune(page.Title)) != 500 || !page.LinksTruncated || len(page.Links) != 2 || len([]rune(page.Links[0].Text)) != 200 {
		t.Fatalf("limits not enforced: %+v", page)
	}
	w.config.CacheBytes = 1
	_, err := w.open(context.Background(), Call{}, openArgs{URL: "https://example.com", MaxChars: ptrWeb(200)})
	if err == nil || !strings.Contains(err.Error(), "cache capacity") {
		t.Fatal(err)
	}
	if w.cacheBytes != 0 || len(w.cache) != 0 {
		t.Fatal("evicted cache accounting incorrect")
	}
}

func ptrWeb(n int) *int { return &n }

func TestOpenCursorOffsetsAndErrorsThroughTool(t *testing.T) {
	w := testWeb(t, WebConfig{})
	w.browser = pageFunc(func(_ context.Context, url string) (agentbrowser.Page, error) {
		return agentbrowser.Page{URL: url, Content: strings.Repeat("a", 300)}, nil
	})
	p := openResult(t, w, "a", "https://example.com", "", 200)
	id, _, _ := strings.Cut(p.NextCursor, ".")
	for _, cursor := range []string{"bad", id + ".nope", id + ".-1", id + ".300", id + ".999999999999999999999999999999"} {
		_, err := w.Tools()[1].Call(context.Background(), Call{Actor: "a", Arguments: mustWebJSON(t, openArgs{URL: "https://example.com", Cursor: &cursor})})
		if err == nil || !strings.Contains(err.Error(), "cursor unavailable") {
			t.Fatal(cursor, err)
		}
	}
}

func TestOpenCancellationAtBackendAndSnapshotBoundaries(t *testing.T) {
	for _, mode := range []string{"read", "snapshot", "continuation", "queued"} {
		t.Run(mode, func(t *testing.T) {
			w := testWeb(t, WebConfig{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w.browser = pageFunc(func(_ context.Context, url string) (agentbrowser.Page, error) {
				if mode == "read" {
					cancel()
				}
				return agentbrowser.Page{URL: url, Content: strings.Repeat("a", 300)}, nil
			})
			args := openArgs{URL: "https://example.com", MaxChars: ptrWeb(200)}
			if mode == "continuation" {
				cursor := openResult(t, w, "", "https://example.com", "", 200).NextCursor
				args.Cursor = &cursor
			}
			if mode == "snapshot" || mode == "continuation" {
				w.now = func() time.Time {
					if mode == "snapshot" {
						w.mu.Lock()
						w.closed = true
						w.mu.Unlock()
					} else {
						cancel()
					}
					return time.Now()
				}
			}
			if mode == "queued" {
				// With a cancelled context and an available slot, either select
				// arm must reject the call before touching the backend.
				cancel()
				w.browser = pageFunc(func(context.Context, string) (agentbrowser.Page, error) {
					t.Error("cancelled read reached backend")
					return agentbrowser.Page{}, nil
				})
			}
			for i := 0; i < 20; i++ {
				_, err := w.open(ctx, Call{}, args)
				if mode == "snapshot" {
					if err == nil || !strings.Contains(err.Error(), "closed") {
						t.Fatal(err)
					}
				} else if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if mode != "queued" {
					break
				}
			}
		})
	}
}

func TestWebCloseHonorsDeadlineWhileCallsDrain(t *testing.T) {
	w := testWeb(t, WebConfig{})
	_, done, err := w.begin(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	done()
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWebBinaryResolution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture-browser")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	for _, tc := range []struct{ given, name, fallback string }{
		{path, "fixture-browser", ""}, {"", "fixture-browser", ""}, {"", "missing", path},
	} {
		got, err := webBinary(tc.given, tc.name, tc.fallback)
		if err != nil || got != path {
			t.Fatal(got, err)
		}
	}
	if _, err := webBinary("", "missing", filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing binary accepted")
	}
}

func TestSearchParserReportsReadFailure(t *testing.T) {
	want := errors.New("HTML stream failed")
	if _, err := parseSearchResults(iotest.ErrReader(want)); !errors.Is(err, want) {
		t.Fatal(err)
	}
}

func TestBrowserBinaryNames(t *testing.T) {
	for _, tc := range []struct{ os, arch, want string }{
		{"darwin", "arm64", "agent-browser-darwin-arm64"},
		{"linux", "amd64", "agent-browser-linux-x64"},
		{"windows", "amd64", "agent-browser-windows-x64.exe"},
	} {
		if got := browserBinaryName(tc.os, tc.arch); got != tc.want {
			t.Fatal(got, tc.want)
		}
	}
}

func TestDefaultBrowserExecutableDiscovery(t *testing.T) {
	const chrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	for _, tc := range []struct {
		os   string
		err  error
		want string
	}{
		{"darwin", nil, chrome}, {"darwin", os.ErrNotExist, ""}, {"linux", nil, ""},
	} {
		got := defaultBrowserExecutable(tc.os, func(path string) (os.FileInfo, error) {
			if tc.os != "darwin" || path != chrome {
				t.Error("unexpected browser probe", path)
			}
			return nil, tc.err
		})
		if got != tc.want {
			t.Fatal(got, tc.want)
		}
	}
	w, err := NewWeb(WebConfig{WKRenderPath: "/missing/worker", AgentBrowserPath: "/missing/browser", BrowserExecutablePath: "/host/custom-browser"})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close(context.Background())
	if w.config.BrowserExecutablePath != "/host/custom-browser" {
		t.Fatal("overrode explicit executable")
	}
}

func TestOpenDefaultAndMaximumChunkSizes(t *testing.T) {
	w := testWeb(t, WebConfig{})
	source := strings.Repeat("界", 20001)
	w.browser = pageFunc(func(_ context.Context, url string) (agentbrowser.Page, error) {
		return agentbrowser.Page{URL: url, Content: source}, nil
	})
	r, err := w.Tools()[1].Call(context.Background(), Call{Arguments: []byte(`{"input":{"url":"https://example.com","cursor":null,"max_chars":null}}`)})
	if err != nil {
		t.Fatal(err)
	}
	var page OpenURLResult
	if err := json.Unmarshal([]byte(r.Content.Text()), &page); err != nil {
		t.Fatal(err)
	}
	if len([]rune(page.Content)) != 20000 || !page.Truncated || page.NextCursor == "" || page.DocumentTruncated {
		t.Fatal("incorrect default chunk", len([]rune(page.Content)), page.Truncated)
	}
	last := openResult(t, w, "", "https://example.com", page.NextCursor, 50000)
	if last.Content != "界" || last.Truncated || last.NextCursor != "" {
		t.Fatal(last)
	}
	whole := openResult(t, w, "", "https://example.com", "", 50000)
	if whole.Content != source || whole.Truncated || whole.NextCursor != "" {
		t.Fatal("maximum chunk lost content")
	}
}

func TestSnapshotCountLimitEvictsOldest(t *testing.T) {
	w := testWeb(t, WebConfig{})
	now := time.Now()
	w.now = func() time.Time { return now }
	w.browser = pageFunc(func(_ context.Context, url string) (agentbrowser.Page, error) {
		return agentbrowser.Page{URL: url, Content: strings.Repeat("a", 300)}, nil
	})
	first := openResult(t, w, "a", "https://example.com", "", 200)
	now = now.Add(time.Second)
	second := openResult(t, w, "a", "https://example.com", "", 200)
	for range 63 {
		now = now.Add(time.Second)
		openResult(t, w, "a", "https://example.com", "", 200)
	}
	if len(w.cache) != 64 {
		t.Fatal("snapshot count limit", len(w.cache))
	}
	if _, err := w.continuePage("a", "https://example.com", first.NextCursor, 200); err == nil {
		t.Fatal("oldest snapshot retained")
	}
	if _, err := w.continuePage("a", "https://example.com", second.NextCursor, 200); err != nil {
		t.Fatal("newer snapshot evicted", err)
	}
	bytes := 0
	for _, snapshot := range w.cache {
		bytes += snapshot.bytes
	}
	if bytes != w.cacheBytes {
		t.Fatal("cache accounting drifted", bytes, w.cacheBytes)
	}
}
