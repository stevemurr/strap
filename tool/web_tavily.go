package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// tavilySearch answers from a search API rather than by driving a browser at a
// search engine. Scraping a public results page is rate limited per address —
// measured here, the first query in a quiet window returns results and an
// immediate second one gets a challenge page — and it needs an engine the site
// will serve, which is what tied search to macOS. An API removes both.
type tavilySearch struct {
	key      string
	endpoint string
	client   *http.Client
}

const tavilyEndpoint = "https://api.tavily.com/search"

type tavilyRequest struct {
	Query       string `json:"query"`
	MaxResults  int    `json:"max_results"`
	SearchDepth string `json:"search_depth"`
}

type tavilyResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

func (t *tavilySearch) Search(ctx context.Context, query string, limit int) ([]SearchHit, error) {
	body, err := json.Marshal(tavilyRequest{Query: query, MaxResults: limit, SearchDepth: "basic"})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+t.key)
	client := t.client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	// The key is in the request, never in an error: a rejected call is quoted
	// back in tool output that reaches the model and the trace.
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("search API rejected the key (HTTP %d)", response.StatusCode)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return nil, fmt.Errorf("search API returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(detail)))
	}
	var decoded tavilyResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}
	hits := make([]SearchHit, 0, len(decoded.Results))
	for _, r := range decoded.Results {
		if r.URL == "" {
			continue
		}
		hits = append(hits, SearchHit{Title: clipRunes(r.Title, 500), URL: r.URL, Snippet: clipRunes(r.Content, 2000)})
	}
	if len(hits) == 0 {
		return nil, errors.New("search returned no usable results")
	}
	return hits, nil
}

func (t *tavilySearch) Close(context.Context) error { return nil }
