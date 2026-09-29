package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/stevemurr/strap/internal/agentbrowser"
)

// tavilyAPI answers from a search API rather than by driving a browser at a
// search engine. Scraping a public results page is rate limited per address —
// measured here, the first query in a quiet window returns results and an
// immediate second one gets a challenge page — and it needs an engine the site
// will serve, which is what tied search to macOS. An API removes both. It also
// reads pages: its extraction fetched 11 of 14 sample pages to the browser's 9,
// PDFs among them, in a fifth of the time and with less site navigation.
type tavilyAPI struct {
	key      string
	endpoint string // search
	extract  string // page extraction
	client   *http.Client
}

const (
	tavilyEndpoint        = "https://api.tavily.com/search"
	tavilyExtractEndpoint = "https://api.tavily.com/extract"
)

type tavilyRequest struct {
	Query       string `json:"query"`
	MaxResults  int    `json:"max_results"`
	SearchDepth string `json:"search_depth"`
	// "markdown" keeps headings, inline code and code blocks, and runs link
	// targets into the prose; "text" is the visible text alone, smaller, with
	// code blocks run into prose.
	IncludeRawContent string `json:"include_raw_content,omitempty"`
}

type tavilyResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
		// Null when the page could not be extracted.
		RawContent *string `json:"raw_content"`
	} `json:"results"`
}

func (t *tavilyAPI) Search(ctx context.Context, query string, limit int, pages string) ([]SearchHit, error) {
	var decoded tavilyResponse
	if err := t.post(ctx, t.endpoint, tavilyRequest{Query: query, MaxResults: limit, SearchDepth: "basic", IncludeRawContent: pages}, &decoded); err != nil {
		return nil, err
	}
	hits := make([]SearchHit, 0, len(decoded.Results))
	for _, r := range decoded.Results {
		if r.URL == "" {
			continue
		}
		hit := SearchHit{Title: clipRunes(r.Title, 500), URL: r.URL, Snippet: clipRunes(r.Content, 2000)}
		if r.RawContent != nil {
			hit.page = *r.RawContent
		}
		hits = append(hits, hit)
	}
	if len(hits) == 0 {
		return nil, errors.New("search returned no usable results")
	}
	return hits, nil
}

type tavilyExtractRequest struct {
	URLs []string `json:"urls"`
	// "basic" and "advanced" returned the same text for every sample page;
	// advanced costs twice as much.
	ExtractDepth string `json:"extract_depth"`
	Format       string `json:"format"`
}

type tavilyExtractResponse struct {
	Results []struct {
		RawContent string `json:"raw_content"`
	} `json:"results"`
	FailedResults []struct {
		Error string `json:"error"`
	} `json:"failed_results"`
}

// Extract reads a page's text through the API, which fetches and cleans it
// without a browser: format "markdown" for an agent, "text" for the visible
// text alone. The API reports no title, final URL or link list; a markdown
// page's links are taken from its text.
func (t *tavilyAPI) Extract(ctx context.Context, page, format string) (agentbrowser.Page, error) {
	var decoded tavilyExtractResponse
	if err := t.post(ctx, t.extract, tavilyExtractRequest{URLs: []string{page}, ExtractDepth: "basic", Format: format}, &decoded); err != nil {
		return agentbrowser.Page{}, err
	}
	if len(decoded.FailedResults) > 0 {
		return agentbrowser.Page{}, fmt.Errorf("could not read the page: %s", clipRunes(decoded.FailedResults[0].Error, 500))
	}
	if len(decoded.Results) == 0 || strings.TrimSpace(decoded.Results[0].RawContent) == "" {
		return agentbrowser.Page{}, errors.New("page returned no readable content")
	}
	text := decoded.Results[0].RawContent
	out := agentbrowser.Page{URL: page, ContentType: "text/plain", Content: text, Links: []WebLink{}}
	if format == "markdown" {
		out.ContentType = "text/markdown"
		out.Links, out.LinksTruncated = markdownLinks(text, page)
	}
	return out, nil
}

var markdownLink = regexp.MustCompile(`(!?)\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)

// markdownLinks lists a markdown page's link destinations, resolved against
// the page, the way a rendered page's are: at most 200, leaving out images and
// anchors within the page.
func markdownLinks(text, page string) ([]WebLink, bool) {
	base, err := url.Parse(page)
	if err != nil {
		return []WebLink{}, false
	}
	links := []WebLink{}
	seen := map[string]bool{}
	for _, m := range markdownLink.FindAllStringSubmatch(text, -1) {
		target, err := base.Parse(m[3])
		if m[1] != "" || strings.HasPrefix(m[3], "#") || err != nil || (target.Scheme != "http" && target.Scheme != "https") {
			continue
		}
		target.Fragment = ""
		if seen[target.String()] {
			continue
		}
		seen[target.String()] = true
		if len(links) == 200 {
			return links, true
		}
		links = append(links, WebLink{Text: strings.TrimSpace(m[2]), URL: target.String()})
	}
	return links, false
}

// post sends one API request. The key is in the request, never in an error: a
// rejected call is quoted back in tool output that reaches the model and the
// trace.
func (t *tavilyAPI) post(ctx context.Context, endpoint string, request, out any) error {
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	post, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	post.Header.Set("Content-Type", "application/json")
	post.Header.Set("Authorization", "Bearer "+t.key)
	client := t.client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(post)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return fmt.Errorf("search API rejected the key (HTTP %d)", response.StatusCode)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return fmt.Errorf("search API returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(detail)))
	}
	// Page text makes a response far larger than snippets alone: eight search
	// results measured 150-260 KB, one extracted page 220 KB.
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(out); err != nil {
		return fmt.Errorf("decode search API response: %w", err)
	}
	return nil
}

func (t *tavilyAPI) Close(context.Context) error { return nil }
