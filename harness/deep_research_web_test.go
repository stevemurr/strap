package harness

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stevemurr/strap/tool"
)

// A research search asks the search API for visible text, not markdown, and
// hands each result's page to the run, so a read needs no browser.
func TestResearchSearchCarriesVisiblePageText(t *testing.T) {
	page := strings.Repeat("The measured latency was 12 ms. ", 1000)
	var requested any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)
		requested = got["include_raw_content"]
		response, _ := json.Marshal(map[string]any{"results": []map[string]any{
			{"title": "Latency", "url": "https://example.test/latency", "content": "snippet", "raw_content": page},
			{"title": "Video", "url": "https://video.test/watch", "content": "snippet", "raw_content": nil},
		}})
		w.Write(response)
	}))
	defer server.Close()
	web, err := tool.NewWeb(tool.WebConfig{TavilyAPIKey: "k", TavilySearchURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer web.Close(context.Background())
	hits, err := researchWebAdapter{web: web, actor: "researcher"}.Search(context.Background(), "latency")
	if err != nil {
		t.Fatal(err)
	}
	if requested != "text" || len(hits) != 2 {
		t.Fatalf("requested %v, hits %+v", requested, hits)
	}
	p := hits[0].Page
	if p == nil || p.Text != page[:researchPageChars] || !p.Truncated || p.ContentType != "text/plain" || p.FinalURL != "https://example.test/latency" {
		t.Fatalf("page: %+v", p)
	}
	if hits[1].Page != nil {
		t.Fatal("a result without page text claimed a page")
	}
}
