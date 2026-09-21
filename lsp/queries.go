package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const outputBytes = 16 << 10
const maxItems = 2000

func (m *Manager) location(s *instance, loc wireLocation, name, kind, container string) (Item, error) {
	uri, r := loc.URI, loc.Range
	var full *wireRange
	if loc.TargetURI != "" {
		uri, r = loc.TargetURI, loc.TargetSelectionRange
		full = &loc.TargetRange
	}
	item := Item{Name: cut(name, 256), Kind: kind, Container: cut(container, 512)}
	path, err := uriPath(uri)
	if err != nil {
		item.URI = uri
		item.Path = uri
		return item, nil
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return item, err
	}
	text, err := textFile(path, m.config.MaxFileBytes)
	if err != nil {
		return item, err
	}
	if d := s.documents[path]; d != nil {
		text = d.text
	} else if hash, ok := s.files[path]; ok && hash != digest([]byte(text)) {
		return item, failure("stale_result", "location changed while collecting results; repeat the query")
	}
	selection, err := fromRange(text, r, s.client.encoding)
	if err != nil {
		return item, err
	}
	item.Path = m.display(path)
	item.Selection = selection
	item.SHA256 = digest([]byte(text))
	item.Excerpt = excerpt(text, selection.Start.Line, selection.Start.Line, 512)
	if full != nil {
		item.Declaration, _ = fromRange(text, *full, s.client.encoding)
	}
	ref := m.id("loc")
	m.refs[ref] = reference{path: path, hash: item.SHA256, key: s.key, generation: s.generation, position: selection.Start, declaration: item.Declaration}
	m.refOrder = append(m.refOrder, ref)
	item.Ref = ref
	for len(m.refOrder) > 4096 {
		delete(m.refs, m.refOrder[0])
		m.refOrder = m.refOrder[1:]
	}
	return item, nil
}

func appendIssue(meta *Metadata, err error) {
	meta.Partial = true
	if len(meta.Issues) < 8 {
		meta.Issues = append(meta.Issues, cut(err.Error(), 256))
	}
}
func (m *Manager) locations(ctx context.Context, s *instance, raw json.RawMessage, meta *Metadata) ([]Item, bool, error) {
	var locs []wireLocation
	if len(raw) > 0 && raw[0] == '{' {
		var loc wireLocation
		if err := decodeRaw(raw, &loc); err != nil {
			return nil, false, err
		}
		locs = []wireLocation{loc}
	} else if err := decodeRaw(raw, &locs); err != nil {
		return nil, false, err
	}
	items := []Item{}
	truncated := len(locs) > maxItems
	for _, loc := range locs[:min(len(locs), maxItems)] {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		item, err := m.location(s, loc, "", "", "")
		if err != nil {
			appendIssue(meta, err)
			continue
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Selection == nil || b.Selection == nil {
			return false
		}
		if a.Selection.Start.Line != b.Selection.Start.Line {
			return a.Selection.Start.Line < b.Selection.Start.Line
		}
		return a.Selection.Start.Column < b.Selection.Start.Column
	})
	return items, truncated, nil
}

func (m *Manager) Symbols(ctx context.Context, q SymbolQuery) (Page, error) {
	ctx, done := m.requestContext(ctx)
	defer done()
	if err := m.acquire(ctx); err != nil {
		return Page{}, err
	}
	defer m.release()
	if strings.TrimSpace(q.Query) == "" {
		return Page{}, failure("invalid_query", "symbol query must not be empty")
	}
	cursor := q.Cursor
	q.Cursor = ""
	key := queryKey("symbols", q)
	if cursor != "" {
		return m.continuePage(key, cursor, q.Limit)
	}
	var servers []*instance
	seen := map[string]bool{}
	meta := Metadata{Freshness: "synchronized", Coverage: "configured/discovered roots, server-selected builds; symbols may exclude locals and unindexed files"}
	items := []Item{}
	truncated := false
	add := func(cfg ServerConfig, path string) {
		matched, err := m.hasLanguageFiles(ctx, cfg, path)
		if err != nil {
			appendIssue(&meta, err)
			return
		}
		if !matched {
			return
		}
		s, err := m.resolve(ctx, cfg, path)
		if err != nil {
			appendIssue(&meta, err)
			return
		}
		if !seen[s.key] {
			servers = append(servers, s)
			seen[s.key] = true
		}
	}
	path := ""
	if q.Path != "" {
		var err error
		path, err = m.path(q.Path)
		if err != nil {
			return Page{}, err
		}
	}
	for _, cfg := range m.config.Servers {
		if path != "" {
			add(cfg, path)
			continue
		}
		if len(cfg.Roots) > 0 {
			for _, root := range cfg.Roots {
				add(cfg, root)
			}
		} else {
			add(cfg, m.config.Dir)
		}
	}
	for _, s := range m.instances {
		if path == "" && !seen[s.key] {
			servers = append(servers, s)
			seen[s.key] = true
		}
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].key < servers[j].key })
	success := 0
	for _, s := range servers {
		if err := m.reconcile(ctx, s); err != nil {
			appendIssue(&meta, err)
			continue
		}
		if err := require(s.client, "workspaceSymbolProvider"); err != nil {
			appendIssue(&meta, err)
			continue
		}
		epoch := m.epoch.Load()
		sm := m.metadata(s)
		meta.Sources = append(meta.Sources, sm.Sources...)
		meta.Partial = meta.Partial || sm.Partial
		for _, issue := range sm.Issues {
			if len(meta.Issues) < 8 {
				meta.Issues = append(meta.Issues, issue)
			}
		}
		var symbols []wireSymbol
		if err := s.client.semanticCall(ctx, "workspace/symbol", map[string]string{"query": q.Query}, &symbols); err != nil {
			appendIssue(&meta, err)
			continue
		}
		success++
		for _, v := range symbols {
			if err := ctx.Err(); err != nil {
				return Page{}, err
			}
			if len(items) >= maxItems {
				truncated = true
				break
			}
			if v.Location == nil {
				appendIssue(&meta, failure("server_error", "workspace symbol has no location"))
				continue
			}
			if path != "" {
				p, e := uriPath(v.Location.URI)
				if e != nil || !within(path, p) {
					continue
				}
			}
			item, err := m.location(s, *v.Location, v.Name, symbolKind(v.Kind), v.Container)
			if err != nil {
				appendIssue(&meta, err)
				continue
			}
			items = append(items, item)
		}
		m.addReadiness(s, &meta)
		m.hasChanged(epoch, &meta)
	}
	if success == 0 {
		return Page{}, failure("server_unavailable", "no symbol query succeeded: %s", strings.Join(meta.Issues, "; "))
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Path < b.Path
	})
	m.checkHashes(items, &meta)
	return m.firstPage(key, items, meta, truncated, q.Limit)
}

func (m *Manager) Outline(ctx context.Context, q OutlineQuery) (Page, error) {
	ctx, done := m.requestContext(ctx)
	defer done()
	if err := m.acquire(ctx); err != nil {
		return Page{}, err
	}
	defer m.release()
	if q.Depth == 0 {
		q.Depth = 1
	}
	if q.Depth < 1 || q.Depth > 8 {
		return Page{}, failure("invalid_query", "outline depth must be 1..8")
	}
	cursor := q.Cursor
	q.Cursor = ""
	key := queryKey("outline", q)
	if cursor != "" {
		return m.continuePage(key, cursor, q.Limit)
	}
	path, err := m.path(q.Path)
	if err != nil {
		return Page{}, err
	}
	s, lang, err := m.forFile(ctx, path)
	if err != nil {
		return Page{}, err
	}
	if err = m.reconcile(ctx, s); err != nil {
		return Page{}, err
	}
	if _, err = m.syncDocument(ctx, s, path, lang); err != nil {
		return Page{}, err
	}
	if err = require(s.client, "documentSymbolProvider"); err != nil {
		return Page{}, err
	}
	epoch := m.epoch.Load()
	meta := m.metadata(s)
	var symbols []wireSymbol
	if err = s.client.semanticCall(ctx, "textDocument/documentSymbol", documentParams(path), &symbols); err != nil {
		return Page{}, err
	}
	items := []Item{}
	truncated := false
	var visit func([]wireSymbol, int, string)
	visit = func(symbols []wireSymbol, depth int, parent string) {
		for _, v := range symbols {
			if ctx.Err() != nil {
				return
			}
			if len(items) >= maxItems {
				truncated = true
				return
			}
			var loc wireLocation
			if v.Location != nil {
				loc = *v.Location
			} else if v.SelectionRange != nil && v.Range != nil {
				loc = wireLocation{TargetURI: fileURI(path), TargetRange: *v.Range, TargetSelectionRange: *v.SelectionRange}
			} else {
				appendIssue(&meta, failure("server_error", "symbol missing range"))
				continue
			}
			container := parent
			if v.Container != "" {
				container = v.Container
			}
			item, e := m.location(s, loc, v.Name, symbolKind(v.Kind), container)
			if e != nil {
				appendIssue(&meta, e)
				continue
			}
			item.Depth = depth
			items = append(items, item)
			if depth < q.Depth {
				visit(v.Children, depth+1, v.Name)
			}
		}
	}
	visit(symbols, 1, "")
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	m.addReadiness(s, &meta)
	m.hasChanged(epoch, &meta)
	m.checkHashes(items, &meta)
	return m.firstPage(key, items, meta, truncated, q.Limit)
}

func (m *Manager) Navigate(ctx context.Context, q NavigateQuery) (Page, error) {
	methods := map[string]string{"definition": "textDocument/definition", "declaration": "textDocument/declaration", "type_definition": "textDocument/typeDefinition", "implementation": "textDocument/implementation"}
	method := methods[q.Relation]
	if method == "" {
		return Page{}, failure("invalid_query", "unknown navigation relation")
	}
	return m.queryLocations(ctx, "navigate", q, q.Target, q.PageQuery, method, operationCapabilities[q.Relation], nil)
}
func (m *Manager) References(ctx context.Context, q ReferenceQuery) (Page, error) {
	return m.queryLocations(ctx, "references", q, q.Target, q.PageQuery, "textDocument/references", "referencesProvider", map[string]any{"includeDeclaration": q.IncludeDeclaration})
}
func (m *Manager) queryLocations(ctx context.Context, kind string, query any, target Target, page PageQuery, method, cap string, refContext any) (Page, error) {
	ctx, done := m.requestContext(ctx)
	defer done()
	if err := m.acquire(ctx); err != nil {
		return Page{}, err
	}
	defer m.release()
	b, _ := json.Marshal(query)
	var keyMap map[string]any
	_ = json.Unmarshal(b, &keyMap)
	keyMap["Cursor"] = ""
	key := queryKey(kind, keyMap)
	if page.Cursor != "" {
		return m.continuePage(key, page.Cursor, page.Limit)
	}
	s, d, path, p, err := m.target(ctx, target)
	if err != nil {
		return Page{}, err
	}
	if err = require(s.client, cap); err != nil {
		return Page{}, err
	}
	epoch := m.epoch.Load()
	meta := m.metadata(s)
	params := positionParams(path, p)
	if refContext != nil {
		params["context"] = refContext
	}
	var raw json.RawMessage
	if err = s.client.semanticCall(ctx, method, params, &raw); err != nil {
		return Page{}, err
	}
	items, truncated, err := m.locations(ctx, s, raw, &meta)
	if err != nil {
		return Page{}, err
	}
	m.addReadiness(s, &meta)
	m.hasChanged(epoch, &meta)
	m.checkHashes([]Item{{Path: m.display(path), SHA256: d.hash}}, &meta)
	m.checkHashes(items, &meta)
	return m.firstPage(key, items, meta, truncated, page.Limit)
}

func (m *Manager) Inspect(ctx context.Context, q InspectQuery) (Inspection, error) {
	ctx, done := m.requestContext(ctx)
	defer done()
	if err := m.acquire(ctx); err != nil {
		return Inspection{}, err
	}
	defer m.release()
	s, d, path, p, err := m.target(ctx, q.Target)
	if err != nil {
		return Inspection{}, err
	}
	if err = require(s.client, "hoverProvider"); err != nil {
		return Inspection{}, err
	}
	epoch := m.epoch.Load()
	meta := m.metadata(s)
	var hover struct {
		Contents json.RawMessage `json:"contents"`
		Range    *wireRange      `json:"range"`
	}
	if err = s.client.semanticCall(ctx, "textDocument/hover", positionParams(path, p), &hover); err != nil {
		return Inspection{}, err
	}
	r := wireRange{p, p}
	if hover.Range != nil {
		r = *hover.Range
	}
	loc, err := m.location(s, wireLocation{URI: fileURI(path), Range: r}, "", "", "")
	if err != nil {
		return Inspection{}, err
	}
	if q.Target.Ref != "" {
		if ref, ok := m.refs[q.Target.Ref]; ok {
			loc.Declaration = copyRange(ref.declaration)
		}
	}
	docs := hoverText(hover.Contents)
	out := Inspection{Location: loc, Documentation: cut(docs, 8000), Truncated: len(docs) > 8000, Metadata: meta}
	if q.IncludeSource {
		start, end := loc.Selection.Start.Line, loc.Selection.Start.Line+8
		if loc.Declaration != nil {
			start, end = loc.Declaration.Start.Line, loc.Declaration.End.Line
		}
		if end-start+1 > 80 {
			end = start + 79
			out.Truncated = true
		}
		out.Location.Excerpt = excerpt(d.text, start, end, 4000)
		if len(out.Location.Excerpt) >= 4000 {
			out.Truncated = true
		}
	} else {
		out.Location.Excerpt = ""
	}
	m.addReadiness(s, &out.Metadata)
	m.hasChanged(epoch, &out.Metadata)
	m.checkHashes([]Item{out.Location}, &out.Metadata)
	return out, nil
}
func hoverText(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	var render func(any) string
	render = func(v any) string {
		switch v := v.(type) {
		case string:
			return v
		case map[string]any:
			s, _ := v["value"].(string)
			return s
		case []any:
			var parts []string
			for _, x := range v {
				parts = append(parts, render(x))
			}
			return strings.Join(parts, "\n\n")
		}
		return ""
	}
	return render(v)
}

func queryKey(kind string, q any) string { b, _ := json.Marshal(q); return kind + ":" + digest(b) }
func pageLimit(n int) (int, error) {
	if n == 0 {
		return 50, nil
	}
	if n < 1 || n > 200 {
		return 0, failure("invalid_query", "limit must be 1..200")
	}
	return n, nil
}
func (m *Manager) firstPage(key string, items []Item, meta Metadata, truncated bool, limit int) (Page, error) {
	if _, err := pageLimit(limit); err != nil {
		return Page{}, err
	}
	budget := min(m.config.CacheBytes/2, 1<<20)
	metaBytes, _ := json.Marshal(meta)
	if len(metaBytes) > outputBytes/2 {
		return Page{}, failure("resource_limit", "result metadata exceeds output budget; narrow the query")
	}
	size := len(metaBytes)
	kept := 0
	for _, item := range items {
		b, _ := json.Marshal(item)
		if size+len(b) > budget {
			truncated = true
			break
		}
		size += len(b)
		kept++
	}
	items = items[:kept]
	if truncated {
		meta.Partial = true
		if len(meta.Issues) < 8 {
			meta.Issues = append(meta.Issues, "result capture limit reached; narrow the query")
		}
	}
	for id, p := range m.pages {
		if time.Since(p.created) > 5*time.Minute {
			m.pageBytes -= p.bytes
			delete(m.pages, id)
		}
	}
	for m.pageBytes+size > m.config.CacheBytes || len(m.pages) >= 64 {
		var oldest string
		var when time.Time
		for id, p := range m.pages {
			if oldest == "" || p.created.Before(when) {
				oldest, when = id, p.created
			}
		}
		if oldest == "" {
			break
		}
		m.pageBytes -= m.pages[oldest].bytes
		delete(m.pages, oldest)
	}
	id := m.id("page")
	m.pages[id] = &retainedPage{key: key, items: items, metadata: meta, epoch: m.epoch.Load(), truncated: truncated, created: time.Now(), bytes: size}
	m.pageBytes += size
	return m.renderPage(id, 0, limit)
}
func (m *Manager) continuePage(key, cursor string, limit int) (Page, error) {
	id, index, ok := strings.Cut(cursor, ":")
	offset, err := strconv.Atoi(index)
	if !ok || err != nil {
		return Page{}, failure("invalid_cursor", "invalid page cursor")
	}
	p := m.pages[id]
	if p == nil || time.Since(p.created) > 5*time.Minute {
		return Page{}, failure("invalid_cursor", "page expired; restart with cursor null")
	}
	if p.key != key {
		return Page{}, failure("invalid_cursor", "continuation requires the same query arguments")
	}
	return m.renderPage(id, offset, limit)
}
func (m *Manager) renderPage(id string, offset, limit int) (Page, error) {
	limit, err := pageLimit(limit)
	if err != nil {
		return Page{}, err
	}
	p := m.pages[id]
	if offset < 0 || offset > len(p.items) {
		return Page{}, failure("invalid_cursor", "invalid page offset")
	}
	meta := p.metadata
	meta.Issues = append([]string(nil), meta.Issues...)
	meta.Sources = append([]Source(nil), meta.Sources...)
	meta.Checks = append([]DiagnosticCheck(nil), meta.Checks...)
	m.hasChanged(p.epoch, &meta)
	out := Page{Items: []Item{}, Truncated: p.truncated, Metadata: meta}
	end := offset
	for end < len(p.items) && end-offset < limit {
		item := p.items[end]
		item.Selection = copyRange(item.Selection)
		item.Declaration = copyRange(item.Declaration)
		out.Items = append(out.Items, item)
		b, _ := json.Marshal(out)
		if len(b) > outputBytes-256 {
			out.Items = out.Items[:len(out.Items)-1]
			break
		}
		end++
	}
	m.checkHashes(out.Items, &out.Metadata)
	if end < len(p.items) {
		if end == offset {
			return Page{}, failure("resource_limit", "one result exceeds the output budget; narrow the query")
		}
		out.NextCursor = fmt.Sprintf("%s:%d", id, end)
	}
	return out, nil
}

func copyRange(r *Range) *Range {
	if r == nil {
		return nil
	}
	v := *r
	return &v
}
