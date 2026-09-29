package tool

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/stevemurr/strap/internal/agentbrowser"
)

type extractFunc func(context.Context, string, string) (agentbrowser.Page, error)

func (f extractFunc) Extract(ctx context.Context, page, format string) (agentbrowser.Page, error) {
	return f(ctx, page, format)
}

// The API's extraction is asked for one page in the requested format; its
// markdown links become the page's link list, resolved against the page.
func TestTavilyExtractReadsAPage(t *testing.T) {
	var got tavilyExtractRequest
	respond := `{"results":[{"url":"https://go.dev/doc/go1.27","raw_content":"# Go 1.27\n\nSee [Go 1.26](/doc/go1.26), [the spec](https://go.dev/ref/spec \"Spec\"), [Go 1.26](/doc/go1.26#top), [below](#changes) and ![logo](/logo.svg)."}],"failed_results":[]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)
		w.Write([]byte(respond))
	}))
	defer server.Close()
	api := &tavilyAPI{key: "k", extract: server.URL, client: server.Client()}
	page, err := api.Extract(context.Background(), "https://go.dev/doc/go1.27", "markdown")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.URLs) != 1 || got.URLs[0] != "https://go.dev/doc/go1.27" || got.Format != "markdown" || got.ExtractDepth != "basic" {
		t.Fatalf("request: %+v", got)
	}
	want := []WebLink{{Text: "Go 1.26", URL: "https://go.dev/doc/go1.26"}, {Text: "the spec", URL: "https://go.dev/ref/spec"}}
	if page.ContentType != "text/markdown" || page.URL != "https://go.dev/doc/go1.27" || page.Title != "" || !strings.HasPrefix(page.Content, "# Go 1.27") || len(page.Links) != 2 || page.Links[0] != want[0] || page.Links[1] != want[1] {
		t.Fatalf("page: %+v", page)
	}
	if page, err = api.Extract(context.Background(), "https://go.dev/doc/go1.27", "text"); err != nil || page.ContentType != "text/plain" || len(page.Links) != 0 {
		t.Fatalf("text page: %+v %v", page, err)
	}
	for body, want := range map[string]string{
		`{"results":[],"failed_results":[{"url":"https://go.dev/x","error":"404 page not found"}]}`: "404 page not found",
		`{"results":[{"url":"https://go.dev/x","raw_content":"  "}],"failed_results":[]}`:           "no readable content",
	} {
		respond = body
		if _, err := api.Extract(context.Background(), "https://go.dev/x", "markdown"); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("want %q, got %v", want, err)
		}
	}
}

// open_url extracts a public page through the API and renders it in the
// browser only when asked, when the API cannot read it, or when it is on a
// private address, whose URL never reaches the API.
func TestOpenURLExtractsFirstAndRendersWhenNeeded(t *testing.T) {
	w := testWeb(t, WebConfig{TavilyAPIKey: "k"})
	if !strings.Contains(w.Tools()[1].Definition().Description, "render true") {
		t.Fatal("description does not offer render")
	}
	var extracted, rendered []string
	var formats []string
	w.extractor = extractFunc(func(_ context.Context, page, format string) (agentbrowser.Page, error) {
		extracted, formats = append(extracted, page), append(formats, format)
		if strings.Contains(page, "unreadable") {
			return agentbrowser.Page{}, errors.New("could not read the page: 404 page not found")
		}
		return agentbrowser.Page{URL: page, ContentType: "text/" + map[string]string{"markdown": "markdown", "text": "plain"}[format], Content: strings.Repeat("extracted ", 50), Links: []WebLink{}}, nil
	})
	w.browser = pageFunc(func(_ context.Context, page string) (agentbrowser.Page, error) {
		rendered = append(rendered, page)
		return agentbrowser.Page{URL: page, Title: "Rendered", ContentType: "text/html", Content: "rendered text"}, nil
	})
	w.lookup = func(_ context.Context, _, host string) ([]netip.Addr, error) {
		switch host {
		case "spark.tail1234.ts.net":
			return []netip.Addr{netip.MustParseAddr("100.101.2.3")}, nil
		case "wiki.corp.example.com":
			return []netip.Addr{netip.MustParseAddr("10.1.2.3")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("93.184.215.14")}, nil
	}
	open := func(url string, render bool) OpenURLResult {
		t.Helper()
		args, _ := MarshalInput(openArgs{URL: url, Render: &render})
		r, err := w.Tools()[1].Call(context.Background(), Call{Actor: "a", Arguments: args})
		if err != nil {
			t.Fatal(url, err)
		}
		var page OpenURLResult
		json.Unmarshal([]byte(r.Content.Text()), &page)
		return page
	}
	page := open("https://example.com/notes", false)
	if page.Rendered || page.ContentType != "text/markdown" || len(extracted) != 1 || formats[0] != "markdown" || len(rendered) != 0 {
		t.Fatalf("public page was not extracted: %+v", page)
	}
	if page = open("https://example.com/discussion", true); !page.Rendered || page.Title != "Rendered" || len(extracted) != 1 {
		t.Fatalf("render did not use the browser alone: %+v", page)
	}
	if page = open("https://example.com/unreadable", false); !page.Rendered || len(extracted) != 2 || len(rendered) != 2 {
		t.Fatalf("unreadable page did not fall back: %+v", page)
	}
	for _, url := range []string{"http://localhost:3000/", "http://127.0.0.1:8080/", "http://10.0.0.5/x", "http://[::1]/", "http://intranet/wiki", "http://printer.local/", "https://spark.tail1234.ts.net/metrics", "https://wiki.corp.example.com/page"} {
		if page = open(url, false); !page.Rendered {
			t.Fatalf("%s was not rendered: %+v", url, page)
		}
	}
	if len(extracted) != 2 {
		t.Fatalf("a private URL reached the API: %v", extracted)
	}
	// A host reads visible text, for exact quotes.
	if _, err := w.OpenPage(context.Background(), "researcher", "https://example.com/source", "", 1000); err != nil || formats[len(formats)-1] != "text" {
		t.Fatalf("host read: %v, formats %v", err, formats)
	}
	// Without a browser, a page the API cannot read reports both reasons.
	w.browserErr = errors.New("agent-browser is not installed")
	args, _ := MarshalInput(openArgs{URL: "https://example.com/unreadable"})
	if _, err := w.Tools()[1].Call(context.Background(), Call{Actor: "a", Arguments: args}); err == nil || !strings.Contains(err.Error(), "404 page not found") || !strings.Contains(err.Error(), "no browser") {
		t.Fatalf("missing-browser fallback: %v", err)
	}
}

// A long extracted page reads on through its cursor without another request.
func TestExtractedPageReadsOnFromItsCursor(t *testing.T) {
	w := testWeb(t, WebConfig{TavilyAPIKey: "k"})
	calls := 0
	text := strings.Repeat("0123456789", 100)
	w.extractor = extractFunc(func(_ context.Context, page, _ string) (agentbrowser.Page, error) {
		calls++
		return agentbrowser.Page{URL: page, ContentType: "text/markdown", Content: text, Links: []WebLink{}}, nil
	})
	w.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.215.14")}, nil
	}
	first := openResult(t, w, "a", "https://example.com/long", "", 300)
	rest := openResult(t, w, "a", "https://example.com/long", first.NextCursor, 5000)
	if calls != 1 || first.Rendered || rest.Rendered || first.Content+rest.Content != text {
		t.Fatalf("calls %d, first %+v, rest %+v", calls, first, rest)
	}
}
