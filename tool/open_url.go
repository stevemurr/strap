package tool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/stevemurr/strap/internal/agentbrowser"
	"github.com/stevemurr/strap/message"
)

type openArgs struct {
	URL      string  `json:"url"`
	MaxChars *int    `json:"max_chars"`
	Cursor   *string `json:"cursor"`
}

type WebLink = agentbrowser.Link

type OpenURLResult struct {
	URL               string    `json:"url"`
	FinalURL          string    `json:"final_url"`
	Title             string    `json:"title"`
	ContentType       string    `json:"content_type"`
	Content           string    `json:"content"`
	Links             []WebLink `json:"links"`
	LinksTruncated    bool      `json:"links_truncated,omitempty"`
	Truncated         bool      `json:"truncated"`          // more retained text is available
	DocumentTruncated bool      `json:"document_truncated"` // tail was not retained
	NextCursor        string    `json:"next_cursor,omitempty"`
}

// Snapshots contain no browser handles. Text, metadata, and links are immutable;
// one cursor always identifies the same offset until eviction/expiry.
type webSnapshot struct {
	owner   message.ActorID
	url     string
	page    agentbrowser.Page
	text    []rune
	created time.Time
	bytes   int
}

func (w *Web) open(ctx context.Context, call Call, args openArgs) (Result, error) {
	u, err := webURL(args.URL)
	if err != nil {
		return Result{}, err
	}
	if len(args.URL) > 8192 {
		return Result{}, errors.New("URL exceeds 8192 bytes")
	}
	requested := u.String()
	limit := 20000
	if args.MaxChars != nil {
		limit = *args.MaxChars
	}
	ctx, done, err := w.begin(ctx, 0)
	if err != nil {
		return Result{}, err
	}
	defer done()
	if args.Cursor != nil {
		page, err := w.continuePage(call.Actor, requested, *args.Cursor, limit)
		if err != nil {
			return Result{}, err
		}
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		return JSON(page)
	}
	if w.browserErr != nil {
		return Result{}, fmt.Errorf("open_url requires agent-browser: %w", w.browserErr)
	}
	queue, cancelQueue := context.WithTimeout(ctx, w.config.OpenQueueTimeout)
	defer cancelQueue()
	select {
	case w.openSlots <- struct{}{}:
	case <-queue.Done():
		return Result{}, fmt.Errorf("open_url queue: %w", queue.Err())
	}
	defer func() { <-w.openSlots }()
	if err := queue.Err(); err != nil {
		return Result{}, fmt.Errorf("open_url queue: %w", err)
	}
	cancelQueue()
	ctx, cancelRead := context.WithTimeout(ctx, w.config.OpenTimeout)
	defer cancelRead()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	page, err := w.browser.Read(ctx, requested)
	if err != nil {
		return Result{}, fmt.Errorf("open_url: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if _, err := webURL(page.URL); err != nil {
		return Result{}, fmt.Errorf("invalid final URL: %w", err)
	}
	if len(page.URL) > 8192 || len(page.ContentType) > 256 {
		return Result{}, errors.New("page URL or content type exceeded metadata limits")
	}
	text := []rune(page.Content)
	if len(text) > w.config.MaxPageChars {
		// Do not retain the oversized backing array in the snapshot cache.
		text = append([]rune(nil), text[:w.config.MaxPageChars]...)
		page.Truncated = true
	}
	page.Content = "" // retain only one copy of the text
	page.Title = clipRunes(page.Title, 500)
	links := make([]WebLink, 0)
	linkBytes := 0
	for _, link := range page.Links {
		if _, err := webURL(link.URL); err != nil {
			continue
		}
		link.Text = clipRunes(link.Text, 200)
		if len(link.URL) > 8192 || linkBytes+len(link.URL)+len(link.Text) > 16<<10 {
			page.LinksTruncated = true
			continue
		}
		links = append(links, link)
		linkBytes += len(link.URL) + len(link.Text)
	}
	page.Links = links
	snapshot := &webSnapshot{owner: call.Actor, url: requested, page: page, text: text, created: w.now(), bytes: 4*len(text) + linkBytes + len(requested) + len(page.URL) + len(page.Title) + len(page.ContentType)}
	id := ""
	if len(text) > limit {
		var nonce [16]byte
		// crypto/rand.Read always fills the buffer and never returns an error.
		rand.Read(nonce[:])
		id = hex.EncodeToString(nonce[:])
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return Result{}, errors.New("web runtime closed")
		}
		w.expireSnapshots()
		for w.cacheBytes+snapshot.bytes > w.config.CacheBytes || len(w.cache) >= 64 {
			var oldest string
			for key, s := range w.cache {
				if oldest == "" || s.created.Before(w.cache[oldest].created) {
					oldest = key
				}
			}
			if oldest == "" {
				w.mu.Unlock()
				return Result{}, errors.New("page exceeds snapshot cache capacity")
			}
			w.removeSnapshot(oldest)
		}
		w.cache[id] = snapshot
		w.cacheBytes += snapshot.bytes
		w.mu.Unlock()
	}
	return JSON(snapshot.chunk(id, 0, limit))
}

func (w *Web) removeSnapshot(id string) {
	w.cacheBytes -= w.cache[id].bytes
	delete(w.cache, id)
}

func (w *Web) expireSnapshots() {
	now := w.now()
	for id, s := range w.cache {
		if !now.Before(s.created.Add(w.config.CacheTTL)) {
			w.removeSnapshot(id)
		}
	}
}

func (w *Web) continuePage(owner message.ActorID, url, cursor string, limit int) (OpenURLResult, error) {
	missing := errors.New("cursor unavailable, expired, or belongs to another URL/agent; reopen the URL without a cursor")
	id, rawOffset, ok := strings.Cut(cursor, ".")
	if !ok {
		return OpenURLResult{}, missing
	}
	offset, err := strconv.Atoi(rawOffset)
	if err != nil || offset < 0 {
		return OpenURLResult{}, missing
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.expireSnapshots()
	s := w.cache[id]
	if s == nil || s.owner != owner || s.url != url || offset >= len(s.text) {
		return OpenURLResult{}, missing
	}
	return s.chunk(id, offset, limit), nil
}

func (s *webSnapshot) chunk(id string, offset, limit int) OpenURLResult {
	end := offset + min(limit, len(s.text)-offset)
	result := OpenURLResult{URL: s.url, FinalURL: s.page.URL, Title: s.page.Title, ContentType: s.page.ContentType, Content: string(s.text[offset:end]), Links: s.page.Links, LinksTruncated: s.page.LinksTruncated, Truncated: end < len(s.text), DocumentTruncated: s.page.Truncated}
	if result.Truncated {
		result.NextCursor = id + "." + strconv.Itoa(end)
	}
	return result
}
