package lsp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (m *Manager) Diagnostics(ctx context.Context, q DiagnosticQuery) (Page, error) {
	ctx, done := m.requestContext(ctx)
	defer done()
	if err := m.acquire(ctx); err != nil {
		return Page{}, err
	}
	defer m.release()
	if q.Paths != nil && (len(q.Paths) == 0 || len(q.Paths) > 32) {
		return Page{}, failure("invalid_query", "provide 1..32 paths, or null for cached diagnostics")
	}
	cursor := q.detachCursor()
	key := queryKey("diagnostics", q)
	if cursor != "" {
		return m.continuePage(key, cursor, q.Limit)
	}
	meta := Metadata{Freshness: "synchronized", Coverage: "requested files, server-selected build; diagnostic completion does not establish whole-project correctness"}
	items := []Item{}
	sources := map[string]bool{}
	type observation struct {
		index      int
		epoch      uint64
		path, hash string
	}
	var observed []observation
	addSource := func(s *instance) {
		if !sources[s.key] {
			sm := m.metadata(s)
			m.addReadiness(s, &sm)
			mergeMetadata(&meta, sm)
			sources[s.key] = true
		}
	}
	add := func(s *instance, path string, d *document, set diagnosticSet, known bool) {
		addSource(s)
		fresh := "unknown"
		if known && s.client.alive() && set.Version != nil && d != nil {
			if *set.Version == d.version && set.Epoch == s.client.epoch.Load() && set.Epoch == m.epoch.Load() {
				fresh = "synchronized"
				b, e := textFile(path, m.config.MaxFileBytes)
				if e != nil || digest([]byte(b)) != d.hash {
					fresh = "stale"
				}
			} else {
				fresh = "stale"
			}
		}
		if !known || fresh != "synchronized" {
			meta.Freshness = "unknown"
			meta.Partial = true
		}
		meta.Checks = append(meta.Checks, DiagnosticCheck{Path: m.display(path), Server: s.config.ID, Freshness: fresh, Count: len(set.Items)})
		if d != nil {
			observed = append(observed, observation{len(meta.Checks) - 1, s.client.epoch.Load(), path, d.hash})
		}
		for _, v := range set.Items {
			if ctx.Err() != nil {
				return
			}
			if len(items) >= maxItems {
				meta.Partial = true
				break
			}
			item := Item{Path: m.display(path)}
			if fresh == "synchronized" {
				located, err := m.location(s, wireLocation{URI: fileURI(path), Range: v.Range}, "", "", "")
				if err != nil {
					appendIssue(&meta, err)
				} else {
					item = located
				}
			} else if d != nil {
				// Unversioned/stale reports are evidence, not reusable source identities.
				// Preserve the message even if its old range no longer fits the document.
				item.Selection, _ = fromRange(d.text, v.Range, s.client.encoding)
			}
			item.Message = cut(v.Message, 2048)
			item.Code = cut(strings.Trim(string(v.Code), "\""), 256)
			item.Source = cut(v.Source, 128)
			item.Freshness = fresh
			severity := []string{"unknown", "error", "warning", "information", "hint"}
			if v.Severity >= 0 && v.Severity < len(severity) {
				item.Severity = severity[v.Severity]
			} else {
				item.Severity = "unknown"
			}
			items = append(items, item)
		}
	}
	if q.Paths == nil {
		meta.Freshness = "unknown"
		meta.Coverage = "cached publications only; unreported files and whole-workspace completeness are unknown"
		for _, s := range m.instances {
			if s.client == nil || !s.client.alive() {
				continue
			}
			sets, gap := s.client.diagnosticSnapshot()
			if gap {
				appendIssue(&meta, failure("resource_limit", "diagnostic cache lost publications"))
			}
			for uri, set := range sets {
				if len(meta.Checks) >= 128 {
					meta.Partial = true
					break
				}
				path, err := uriPath(uri)
				if err != nil {
					appendIssue(&meta, err)
					continue
				}
				d := s.documents[path]
				if d != nil {
					b, e := readBounded(path, m.config.MaxFileBytes)
					if e != nil || digest(b) != d.hash {
						d = nil
					}
				}
				add(s, path, d, set, true)
			}
		}
	} else {
		// One deadline for diagnostic waiting across the requested files, in
		// addition to the transaction deadline for startup and synchronization.
		var waitUntil time.Time
		for _, input := range q.Paths {
			path, err := m.path(input)
			if err != nil {
				appendIssue(&meta, err)
				meta.Freshness = "unknown"
				meta.Checks = append(meta.Checks, DiagnosticCheck{Path: input, Freshness: "unknown"})
				continue
			}
			s, lang, err := m.forFile(ctx, path)
			if err != nil {
				appendIssue(&meta, err)
				meta.Freshness = "unknown"
				continue
			}
			if err = m.reconcile(ctx, s); err != nil {
				appendIssue(&meta, err)
				meta.Freshness = "unknown"
				continue
			}
			d, err := m.syncDocument(ctx, s, path, lang)
			if err != nil {
				appendIssue(&meta, err)
				meta.Freshness = "unknown"
				continue
			}
			if waitUntil.IsZero() {
				waitUntil = time.Now().Add(milliseconds(m.config.DiagnosticTimeoutMS))
			}
			wait, stop := context.WithDeadline(ctx, waitUntil)
			set, known := m.documentDiagnostics(wait, s, path, d)
			stop()
			add(s, path, d, set, known)
		}
	}
	// Recheck every requested snapshot, including clean (empty) reports. A
	// later file's synchronization can invalidate an earlier dependent-file check.
	for _, observation := range observed {
		text, err := textFile(observation.path, m.config.MaxFileBytes)
		if err == nil && digest([]byte(text)) == observation.hash && observation.epoch == m.epoch.Load() {
			continue
		}
		check := &meta.Checks[observation.index]
		check.Freshness = "stale"
		meta.Freshness = "stale"
		meta.Partial = true
		for i := range items {
			if items[i].Path == check.Path {
				items[i].Freshness = "stale"
				items[i].Ref = ""
			}
		}
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Selection != nil && b.Selection != nil && a.Selection.Start.Line != b.Selection.Start.Line {
			return a.Selection.Start.Line < b.Selection.Start.Line
		}
		return a.Message < b.Message
	})
	sort.Slice(meta.Checks, func(i, j int) bool {
		a, b := meta.Checks[i], meta.Checks[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Server < b.Server
	})
	m.checkHashes(items, &meta)
	if ctx.Err() != nil {
		return Page{}, ctx.Err()
	}
	return m.firstPage(key, items, meta, len(items) >= maxItems, q.Limit)
}

func (m *Manager) documentDiagnostics(ctx context.Context, s *instance, path string, d *document) (diagnosticSet, bool) {
	c := s.client
	uri := fileURI(path)
	if c.supported("diagnosticProvider") {
		if !c.waitAnalysis(ctx, d.changedAt) {
			sets, _ := c.diagnosticSnapshot()
			return sets[uri], false
		}
		var report struct {
			Kind  string           `json:"kind"`
			Items []wireDiagnostic `json:"items"`
		}
		params := documentParams(path)
		var options struct {
			Identifier string `json:"identifier"`
		}
		_ = json.Unmarshal(c.caps["diagnosticProvider"], &options)
		if options.Identifier != "" {
			params["identifier"] = options.Identifier
		}
		revision := c.diagnosticRevision.Load()
		if err := c.call(ctx, "textDocument/diagnostic", params, &report); err == nil && report.Kind == "full" {
			version := d.version
			set := diagnosticSet{Version: &version, Items: report.Items, Received: time.Now(), Epoch: c.epoch.Load(), Revision: revision}
			c.storeDiagnosticSet(uri, set, true)
			sets, _ := c.diagnosticSnapshot()
			return sets[uri], true
		}
	}
	for {
		c.mu.Lock()
		set, ok := c.diagnostics[uri]
		c.mu.Unlock()
		if ok && set.Version != nil && *set.Version == d.version && set.Epoch == c.epoch.Load() {
			return set, true
		}
		select {
		case <-ctx.Done():
			return set, ok
		case <-c.done:
			return set, false
		case <-c.changed:
		}
	}
}

// CachedSummary never launches or queries a server. It can be appended at the
// next tool boundary without blocking editing on language analysis.
func (m *Manager) CachedSummary(ctx context.Context) (string, error) {
	if err := m.acquire(ctx); err != nil {
		return "", err
	}
	defer m.release()
	epoch := m.epoch.Load()
	lines := []string{}
	for _, s := range m.instances {
		c := s.client
		if c == nil || !c.alive() {
			continue
		}
		sets, _ := c.diagnosticSnapshot()
		for uri, set := range sets {
			if ctx.Err() != nil {
				break
			}
			path, err := uriPath(uri)
			if err != nil {
				continue
			}
			d := s.documents[path]
			if d == nil || set.Version == nil || *set.Version != d.version || set.Epoch != epoch {
				continue
			}
			text, err := textFile(path, m.config.MaxFileBytes)
			if err != nil || digest([]byte(text)) != d.hash {
				continue
			}
			for _, item := range set.Items {
				if item.Severity != 1 && item.Severity != 2 {
					continue
				}
				lines = append(lines, m.display(path)+":"+strconv.Itoa(item.Range.Start.Line+1)+": "+cut(item.Message, 256))
				if len(lines) >= 5 {
					break
				}
			}
			if len(lines) >= 5 {
				break
			}
		}
		if len(lines) >= 5 {
			break
		}
	}
	if epoch != m.epoch.Load() {
		return "", nil
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n"), nil
}

// RefreshChanged drains coalesced host hints and refreshes already opened files.
// Explicit diagnostics remain available for files that haven't been queried yet.
func (m *Manager) refresh() {
	defer m.wg.Done()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	pending := map[string]bool{}
	all := false
	for {
		select {
		case <-m.life.Done():
			return
		case path := <-m.changes:
			if path == "" {
				all = true
			} else if len(pending) < 32 {
				pending[path] = true
			} else {
				all = true
			}
		case <-ticker.C:
			if len(pending) == 0 && !all {
				continue
			}
			paths := []string{}
			ctx, cancel := context.WithTimeout(m.life, 100*time.Millisecond)
			if err := m.acquire(ctx); err != nil {
				cancel()
				continue
			} else {
				for _, s := range m.instances {
					if s.client == nil || !s.client.alive() {
						continue
					}
					for path := range s.documents {
						if all || pending[path] || pending[m.display(path)] {
							paths = append(paths, path)
							if len(paths) >= 32 {
								break
							}
						}
					}
					if len(paths) >= 32 {
						break
					}
				}
				m.release()
			}
			cancel()
			pending = map[string]bool{}
			all = false
			if len(paths) == 0 {
				continue
			}
			ctx, cancel = context.WithTimeout(m.life, milliseconds(m.config.RequestTimeoutMS))
			_, _ = m.Diagnostics(ctx, DiagnosticQuery{Paths: paths, PageQuery: PageQuery{Limit: 5}})
			cancel()
		}
	}
}

func (c *client) storeDiagnosticSet(uri string, set diagnosticSet, pulled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cache := c.diagnostics
	if pulled {
		cache = c.pulledDiagnostics
	}
	old, ok := cache[uri]
	if ok && old.Version != nil && set.Version != nil && *set.Version < *old.Version {
		return
	}
	b, _ := json.Marshal(set.Items)
	oldSize := 0
	if ok {
		v, _ := json.Marshal(old.Items)
		oldSize = len(v)
	}
	if len(set.Items) > 2000 || (!ok && len(c.diagnostics)+len(c.pulledDiagnostics) >= 2048) || c.diagnosticBytes-oldSize+len(b) > c.diagnosticLimit {
		c.diagnosticGap = true
		return
	}
	c.diagnosticBytes += len(b) - oldSize
	cache[uri] = set
}

func canonicalChanged(dir, path string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	if p, err := filepath.EvalSymlinks(path); err == nil {
		return p
	}
	return filepath.Clean(path)
}

// Some peers (notably rust-analyzer) pull native diagnostics and push compiler
// diagnostics. A pull must not erase the independently published compiler set.
func (c *client) diagnosticSnapshot() (map[string]diagnosticSet, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]diagnosticSet, len(c.diagnostics)+len(c.pulledDiagnostics))
	for uri, set := range c.diagnostics {
		out[uri] = set
	}
	for uri, set := range c.pulledDiagnostics {
		if set.Revision != c.diagnosticRevision.Load() {
			set.Version = nil
		}
		if pushed, ok := out[uri]; ok {
			set = mergeDiagnostics(pushed, set)
		}
		out[uri] = set
	}
	return out, c.diagnosticGap
}
func mergeDiagnostics(a, b diagnosticSet) diagnosticSet {
	out := b
	if a.Version == nil || b.Version == nil || *a.Version != *b.Version || a.Epoch != b.Epoch {
		out.Version = nil
	}
	out.Items = nil
	seen := map[string]bool{}
	for _, items := range [][]wireDiagnostic{a.Items, b.Items} {
		for _, item := range items {
			raw, _ := json.Marshal(item)
			key := string(raw)
			if !seen[key] {
				seen[key] = true
				out.Items = append(out.Items, item)
			}
		}
	}
	return out
}
