package tool

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/stevemurr/strap/identity"
)

// SearchWeb and OpenPage share the model tools' validation, lifetime and slots.
// Host consumers can retain evidence without adding it to an agent's history.
// With chars, SearchWeb returns up to chars of each result's visible page text
// when the backend has it, marked truncated when the page is longer; zero
// returns snippets alone. Nothing is retained for a cursor.
func (w *Web) SearchWeb(ctx context.Context, query string, limit, chars int) (WebSearchResult, error) {
	if limit < 1 || limit > 10 {
		return WebSearchResult{}, errors.New("search limit must be 1..10")
	}
	if chars != 0 && (chars < 200 || chars > 50000) {
		return WebSearchResult{}, errors.New("page characters must be 0 or 200..50000")
	}
	pages := ""
	if chars > 0 {
		pages = "text"
	}
	return w.find(ctx, "", searchArgs{Query: query, MaxResults: &limit}, pages, chars)
}

func (w *Web) OpenPage(ctx context.Context, actor identity.ActorID, url, cursor string, chars int) (OpenURLResult, error) {
	if chars < 200 || chars > 50000 {
		return OpenURLResult{}, errors.New("page characters must be 200..50000")
	}
	args := openArgs{URL: url, MaxChars: &chars}
	if cursor != "" {
		args.Cursor = &cursor
	}
	// Visible text, not markdown: a research claim quotes an exact excerpt.
	r, err := w.openAs(ctx, actor, args, "text")
	var out OpenURLResult
	if err == nil {
		err = json.Unmarshal([]byte(r.Content.Text()), &out)
	}
	return out, err
}
