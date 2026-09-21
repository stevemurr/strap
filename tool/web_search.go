package tool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

type searchArgs struct {
	Query      string `json:"query"`
	MaxResults *int   `json:"max_results"`
}

type SearchHit struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}
type WebSearchResult struct {
	Query   string      `json:"query"`
	Results []SearchHit `json:"results"`
}

func (w *Web) search(ctx context.Context, _ Call, args searchArgs) (Result, error) {
	query := strings.TrimSpace(args.Query)
	if query == "" || len(query) > 8192 {
		return Result{}, errors.New("query must contain 1..8192 bytes of nonblank text")
	}
	if w.searchErr != nil {
		return Result{}, fmt.Errorf("web_search requires a backend: set TAVILY_API_KEY for the search API, or install wkrender: %w", w.searchErr)
	}
	ctx, done, err := w.begin(ctx, w.config.SearchTimeout)
	if err != nil {
		return Result{}, err
	}
	defer done()
	limit := 8
	if args.MaxResults != nil {
		limit = *args.MaxResults
	}
	hits, err := w.worker.Search(ctx, query, limit)
	if err != nil {
		return Result{}, fmt.Errorf("web_search: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return JSON(WebSearchResult{Query: query, Results: hits[:min(limit, len(hits))]})
}

func parseSearchResults(source io.Reader) ([]SearchHit, error) {
	doc, err := html.Parse(source)
	if err != nil {
		return nil, fmt.Errorf("parse search HTML: %w", err)
	}
	var found []SearchHit
	empty, challenge := false, false
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			classes := strings.Fields(htmlAttr(n, "class"))
			for _, class := range classes {
				switch class {
				case "no-results", "no-results__message":
					empty = true
				case "anomaly-modal", "anomaly-modal__modal", "g-recaptcha", "h-captcha":
					challenge = true
				case "result__a":
					if n.Data == "a" {
						found = append(found, SearchHit{Title: clipRunes(htmlText(n), 500), URL: searchDestination(htmlAttr(n, "href"))})
					}
				case "result__snippet":
					if len(found) > 0 && found[len(found)-1].Snippet == "" {
						found[len(found)-1].Snippet = clipRunes(htmlText(n), 2000)
					}
				}
			}
			if n.Data == "form" && (htmlAttr(n, "id") == "challenge-form" || strings.Contains(htmlAttr(n, "action"), "anomaly.js")) {
				challenge = true
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	if challenge {
		return nil, errors.New("search engine returned a challenge page instead of usable results")
	}
	hits := make([]SearchHit, 0, len(found))
	seen := map[string]bool{}
	for _, hit := range found {
		u, err := webURL(hit.URL)
		if err != nil || duckduckgo(u.Hostname()) || len(hit.URL) > 8192 {
			continue
		}
		u.Fragment = ""
		key := u.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		hits = append(hits, hit)
	}
	if len(hits) == 0 && !empty {
		return nil, errors.New("search engine returned unrecognized markup; could not extract results")
	}
	return hits, nil
}

func searchDestination(raw string) string {
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if duckduckgo(u.Hostname()) {
		if target := u.Query().Get("uddg"); target != "" {
			return target
		}
	}
	return raw
}

func duckduckgo(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host == "duckduckgo.com" || strings.HasSuffix(host, ".duckduckgo.com")
}

func htmlAttr(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func htmlText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func clipRunes(text string, limit int) string {
	runes := []rune(text)
	return string(runes[:min(len(runes), limit)])
}
