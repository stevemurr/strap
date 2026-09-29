package tool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
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
	Render   *bool   `json:"render"`
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
	Rendered          bool      `json:"rendered"`           // a browser rendered the page; otherwise the search API extracted its text
	NextCursor        string    `json:"next_cursor,omitempty"`
}

// Snapshots contain no browser handles. Text, metadata, and links are immutable;
// one cursor always identifies the same offset until eviction/expiry.
type webSnapshot struct {
	owner    message.ActorID
	url      string
	page     agentbrowser.Page
	rendered bool
	text     []rune
	created  time.Time
	bytes    int
}

// An agent reads extracted pages as markdown, which keeps headings and code.
func (w *Web) open(ctx context.Context, call Call, args openArgs) (Result, error) {
	return w.openAs(ctx, call.Actor, args, "markdown")
}

// openAs opens a page for owner; format is the form of extracted text.
func (w *Web) openAs(ctx context.Context, owner message.ActorID, args openArgs, format string) (Result, error) {
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
		page, err := w.continuePage(owner, requested, *args.Cursor, limit)
		if err != nil {
			return Result{}, err
		}
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		return JSON(page)
	}
	page, rendered, err := w.read(ctx, u, format, args.Render != nil && *args.Render)
	if err != nil {
		return Result{}, err
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
	snapshot := &webSnapshot{owner: owner, url: requested, page: page, rendered: rendered, text: text, created: w.now(), bytes: 4*len(text) + linkBytes + len(requested) + len(page.URL) + len(page.Title) + len(page.ContentType)}
	id := ""
	if len(text) > limit {
		if id, err = w.retain(snapshot); err != nil {
			return Result{}, err
		}
	}
	return JSON(snapshot.chunk(id, 0, limit))
}

// read gets a page's text, extracted by the search API when there is one and
// otherwise rendered in the browser. A page on a private address is always
// rendered, so its URL never leaves this machine; so is a page the agent asks
// to render, and one the API cannot read. rendered says which way it went.
func (w *Web) read(ctx context.Context, u *url.URL, format string, render bool) (page agentbrowser.Page, rendered bool, err error) {
	requested := u.String()
	if w.extractor == nil || render || w.private(ctx, u) {
		page, err = w.render(ctx, requested)
		return page, true, err
	}
	extract, cancel := context.WithTimeout(ctx, w.config.OpenTimeout)
	page, err = w.extractor.Extract(extract, requested, format)
	cancel()
	if err == nil || ctx.Err() != nil {
		return page, false, err
	}
	if w.browserErr != nil {
		return agentbrowser.Page{}, false, fmt.Errorf("open_url: search API: %w; no browser is installed to render the page instead", err)
	}
	page, renderErr := w.render(ctx, requested)
	if renderErr != nil {
		return agentbrowser.Page{}, true, fmt.Errorf("search API: %v; rendering the page instead failed: %w", err, renderErr)
	}
	return page, true, nil
}

// render reads a page in the browser once one of the shared slots is free.
func (w *Web) render(ctx context.Context, requested string) (agentbrowser.Page, error) {
	if w.browserErr != nil {
		return agentbrowser.Page{}, fmt.Errorf("open_url requires agent-browser: %w", w.browserErr)
	}
	queue, cancelQueue := context.WithTimeout(ctx, w.config.OpenQueueTimeout)
	defer cancelQueue()
	select {
	case w.openSlots <- struct{}{}:
	case <-queue.Done():
		return agentbrowser.Page{}, fmt.Errorf("open_url queue: %w", queue.Err())
	}
	defer func() { <-w.openSlots }()
	if err := queue.Err(); err != nil {
		return agentbrowser.Page{}, fmt.Errorf("open_url queue: %w", err)
	}
	cancelQueue()
	ctx, cancelRead := context.WithTimeout(ctx, w.config.OpenTimeout)
	defer cancelRead()
	if err := ctx.Err(); err != nil {
		return agentbrowser.Page{}, err
	}
	page, err := w.browser.Read(ctx, requested)
	if err != nil {
		return agentbrowser.Page{}, fmt.Errorf("open_url: %w", err)
	}
	return page, ctx.Err()
}

// sharedAddressSpace is carrier-grade NAT's range, which Tailscale also uses
// for the machines on a tailnet.
var sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")

// private reports whether a URL names this machine or a private network,
// whose pages the search API cannot reach and must not be sent. A name is
// looked up, so one that resolves to a private address counts; a name that
// does not resolve does not.
func (w *Web) private(ctx context.Context, u *url.URL) bool {
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if ip, err := netip.ParseAddr(host); err == nil {
		return !publicAddr(ip)
	}
	if !strings.Contains(host, ".") {
		return true
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".lan", ".home.arpa"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	lookup, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	addrs, err := w.lookup(lookup, "ip", host)
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		if !publicAddr(addr) {
			return true
		}
	}
	return false
}

func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !sharedAddressSpace.Contains(ip)
}

// retain caches a snapshot for cursor reads, evicting the oldest to make room,
// and returns its id.
func (w *Web) retain(snapshot *webSnapshot) (string, error) {
	var nonce [16]byte
	// crypto/rand.Read always fills the buffer and never returns an error.
	rand.Read(nonce[:])
	id := hex.EncodeToString(nonce[:])
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return "", errors.New("web runtime closed")
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
			return "", errors.New("page exceeds snapshot cache capacity")
		}
		w.removeSnapshot(oldest)
	}
	w.cache[id] = snapshot
	w.cacheBytes += snapshot.bytes
	return id, nil
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
	result := OpenURLResult{URL: s.url, FinalURL: s.page.URL, Title: s.page.Title, ContentType: s.page.ContentType, Content: string(s.text[offset:end]), Links: s.page.Links, LinksTruncated: s.page.LinksTruncated, Truncated: end < len(s.text), DocumentTruncated: s.page.Truncated, Rendered: s.rendered}
	if result.Truncated {
		result.NextCursor = id + "." + strconv.Itoa(end)
	}
	return result
}
