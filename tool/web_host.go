package tool

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/stevemurr/strap/identity"
)

// SearchWeb and OpenPage share the model tools' validation, lifetime and slots.
// Host consumers can retain evidence without adding it to an agent's history.
func (w *Web) SearchWeb(ctx context.Context, query string, limit int) (WebSearchResult, error) {
	if limit < 1 || limit > 10 {
		return WebSearchResult{}, errors.New("search limit must be 1..10")
	}
	r, err := w.search(ctx, Call{}, searchArgs{Query: query, MaxResults: &limit})
	var out WebSearchResult
	if err == nil {
		err = json.Unmarshal([]byte(r.Content.Text()), &out)
	}
	return out, err
}

func (w *Web) OpenPage(ctx context.Context, actor identity.ActorID, url, cursor string, chars int) (OpenURLResult, error) {
	if chars < 200 || chars > 50000 {
		return OpenURLResult{}, errors.New("page characters must be 200..50000")
	}
	args := openArgs{URL: url, MaxChars: &chars}
	if cursor != "" {
		args.Cursor = &cursor
	}
	r, err := w.open(ctx, Call{Actor: actor}, args)
	var out OpenURLResult
	if err == nil {
		err = json.Unmarshal([]byte(r.Content.Text()), &out)
	}
	return out, err
}
