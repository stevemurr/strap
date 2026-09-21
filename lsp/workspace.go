package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// reconcile always verifies workspace content before a query. Watchers are hints,
// not proof of freshness; this also catches coalesced events and shell changes.
func (m *Manager) reconcile(ctx context.Context, s *instance) error {
	if err := m.ensure(ctx, s); err != nil {
		return err
	}
	before := m.epoch.Load()
	files := map[string]string{}
	dirs := map[string]bool{}
	count, bytesRead := 0, 0
	s.partial = false
	s.issues = nil
	gap := func(reason string) {
		s.partial = true
		if len(s.issues) < 8 {
			s.issues = append(s.issues, reason)
		}
	}
	err := filepath.WalkDir(s.spec.Root, func(path string, d fs.DirEntry, e error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e != nil {
			gap("some workspace paths could not be read")
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		count++
		if count > m.config.MaxWorkspaceFiles || bytesRead > m.config.MaxWorkspaceBytes {
			gap("workspace scan budget exceeded")
			return fs.SkipAll
		}
		if d.IsDir() {
			if path != s.spec.Root {
				if excludedDirectory(d.Name()) {
					return fs.SkipDir
				}
			}
			dirs[path] = true
			if m.watched[path] || len(m.watched) < m.config.MaxWorkspaceFiles*m.config.MaxServers { // Re-add recreated directories.
				if e := m.watcher.Add(path); e != nil {
					gap("filesystem watch unavailable; queries still rescan")
				} else {
					m.watched[path] = true
				}
			} else {
				gap("filesystem watch budget exceeded")
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			gap("symlinked workspace entries are not included in scans")
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		// Include build/configuration files, even if no server handles their suffix.
		b, e := readBounded(path, min(m.config.MaxFileBytes, m.config.MaxWorkspaceBytes-bytesRead))
		if e != nil {
			gap("some workspace files exceed the scan budget or could not be read")
			return nil
		}
		bytesRead += len(b)
		files[path] = digest(b)
		return nil
	})
	if err != nil {
		return err
	}
	if !s.partial {
		for path := range m.watched {
			if within(s.spec.Root, path) && !dirs[path] {
				_ = m.watcher.Remove(path)
				delete(m.watched, path)
			}
		}
	}
	if m.watchGap.Swap(false) {
		gap("a filesystem event was lost; bounded workspace reconciliation was performed")
	}
	var changes []map[string]any
	for path, hash := range files {
		old, ok := s.files[path]
		if !ok {
			if s.scanned {
				changes = append(changes, map[string]any{"uri": fileURI(path), "type": 1})
			}
		} else if old != hash {
			changes = append(changes, map[string]any{"uri": fileURI(path), "type": 2})
		}
	}
	// An incomplete scan cannot establish absence.
	if !s.partial {
		for path := range s.files {
			if _, ok := files[path]; !ok {
				changes = append(changes, map[string]any{"uri": fileURI(path), "type": 3})
			}
		}
	} else {
		for path, hash := range s.files {
			if _, ok := files[path]; !ok && len(files) < m.config.MaxWorkspaceFiles {
				files[path] = hash
			}
		}
	}
	if len(changes) > 0 {
		m.epoch.Add(1)
	}
	s.client.epoch.Store(m.epoch.Load())
	// Only notify registered glob/kind interests, in bounded batches.
	observedChanges := len(changes)
	changes = s.client.watchedChanges(changes)
	for start := 0; start < len(changes); start += 100 {
		if err := s.client.notify(ctx, "workspace/didChangeWatchedFiles", map[string]any{"changes": changes[start:min(start+100, len(changes))]}); err != nil {
			return err
		}
	}
	s.files = files
	s.scanned = true
	paths := make([]string, 0, len(s.documents))
	for p := range s.documents {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, path := range paths {
		d := s.documents[path]
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if s.client.openClose {
				if err = s.client.notify(ctx, "textDocument/didClose", map[string]any{"textDocument": map[string]string{"uri": fileURI(path)}}); err != nil {
					return err
				}
			}
			delete(s.documents, path)
			continue
		}
		if _, err := m.syncDocument(ctx, s, path, d.language); err != nil {
			return err
		}
	}
	if m.epoch.Load() != before && observedChanges == 0 {
		gap("workspace notifications arrived during reconciliation")
	}
	return nil
}

func (m *Manager) syncDocument(ctx context.Context, s *instance, path, language string) (*document, error) {
	text, err := textFile(path, m.config.MaxFileBytes)
	if err != nil {
		return nil, err
	}
	hash := digest([]byte(text))
	c := s.client
	d := s.documents[path]
	if d != nil && d.hash == hash {
		m.sequence++
		d.used = m.sequence
		return d, nil
	}
	if d == nil {
		if len(s.documents) >= m.config.MaxOpenDocuments {
			var old string
			var age uint64
			for p, v := range s.documents {
				if old == "" || v.used < age {
					old, age = p, v.used
				}
			}
			if c.openClose {
				if err = c.notify(ctx, "textDocument/didClose", map[string]any{"textDocument": map[string]string{"uri": fileURI(old)}}); err != nil {
					return nil, err
				}
			}
			delete(s.documents, old)
		}
		s.nextVersion++
		d = &document{version: s.nextVersion, language: language}
		if c.openClose {
			if err = c.notify(ctx, "textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": fileURI(path), "languageId": language, "version": d.version, "text": text}}); err != nil {
				return nil, err
			}
		}
	} else {
		copy := *d
		d = &copy
		s.nextVersion++
		d.version = s.nextVersion
		change := map[string]any{"text": text}
		if c.syncKind == 2 {
			change["range"] = wireRange{wirePosition{}, endPosition(d.text, c.encoding)}
		}
		if c.syncKind != 0 {
			if err = c.notify(ctx, "textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": fileURI(path), "version": d.version}, "contentChanges": []any{change}}); err != nil {
				return nil, err
			}
		} else if c.openClose {
			if err = c.notify(ctx, "textDocument/didClose", map[string]any{"textDocument": map[string]string{"uri": fileURI(path)}}); err != nil {
				return nil, err
			}
			if err = c.notify(ctx, "textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": fileURI(path), "languageId": language, "version": d.version, "text": text}}); err != nil {
				return nil, err
			}
		}
	}
	if c.save {
		p := map[string]any{"textDocument": map[string]string{"uri": fileURI(path)}}
		if c.saveText {
			p["text"] = text
		}
		if err = c.notify(ctx, "textDocument/didSave", p); err != nil {
			return nil, err
		}
	}
	m.sequence++
	d.text = text
	d.changedAt = time.Now()
	d.hash = hash
	d.used = m.sequence
	s.documents[path] = d
	return d, nil
}

func (m *Manager) target(ctx context.Context, t Target) (*instance, *document, string, wirePosition, error) {
	if err := validateTarget(t); err != nil {
		return nil, nil, "", wirePosition{}, err
	}
	var s *instance
	var path, lang string
	var err error
	if t.Ref != "" {
		r, ok := m.refs[t.Ref]
		if !ok {
			return nil, nil, "", wirePosition{}, failure("stale_reference", "reference expired; rediscover the location")
		}
		s = m.instances[r.key]
		if s == nil || s.client == nil || !s.client.alive() || s.generation != r.generation {
			return nil, nil, "", wirePosition{}, failure("stale_reference", "server restarted; rediscover the location")
		}
		path = r.path
		t.Line = r.position.Line
		t.Column = r.position.Column
		b, e := readBounded(path, m.config.MaxFileBytes)
		if e != nil || digest(b) != r.hash {
			return nil, nil, "", wirePosition{}, failure("stale_reference", "document changed; rediscover the location")
		}
		lang = language(s.config, path)
	} else {
		path, err = m.path(t.Path)
		if err != nil {
			return nil, nil, "", wirePosition{}, err
		}
		s, lang, err = m.forFile(ctx, path)
		if err != nil {
			return nil, nil, "", wirePosition{}, err
		}
	}
	if err = m.reconcile(ctx, s); err != nil {
		return nil, nil, "", wirePosition{}, err
	}
	d, err := m.syncDocument(ctx, s, path, lang)
	if err != nil {
		return nil, nil, "", wirePosition{}, err
	}
	if t.Ref != "" && m.refs[t.Ref].hash != d.hash {
		return nil, nil, "", wirePosition{}, failure("stale_reference", "document changed while synchronizing")
	}
	if t.Symbol != "" {
		position, err := symbolPosition(d.text, d.language, t.Line, t.Symbol, t.Context)
		if err != nil {
			return nil, nil, "", wirePosition{}, err
		}
		t.Column = position.Column
	}
	p, err := toWire(d.text, Position{t.Line, t.Column}, s.client.encoding)
	if err != nil {
		return nil, nil, "", wirePosition{}, err
	}
	if t.ExpectedText != nil {
		ls := lines(d.text)
		r := []rune(strings.TrimSuffix(ls[t.Line-1], "\r"))
		if !strings.HasPrefix(string(r[t.Column-1:]), *t.ExpectedText) {
			return nil, nil, "", wirePosition{}, failure("invalid_position", "expected_text does not match at this position")
		}
	}
	return s, d, path, p, nil
}

func (m *Manager) checkHashes(items []Item, meta *Metadata) {
	seen := map[string]bool{}
	for _, item := range items {
		if item.SHA256 == "" || seen[item.Path] {
			continue
		}
		seen[item.Path] = true
		p := item.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(m.config.Dir, p)
		}
		b, err := readBounded(p, m.config.MaxFileBytes)
		if err != nil || digest(b) != item.SHA256 {
			meta.Freshness = "stale"
			meta.Issues = append(meta.Issues, "a result document changed during the query")
			return
		}
	}
}

func require(c *client, cap string) error {
	if !c.supported(cap) {
		return failure("unsupported", "server does not advertise %s", cap)
	}
	return nil
}
func documentParams(path string) map[string]any {
	return map[string]any{"textDocument": map[string]string{"uri": fileURI(path)}}
}
func positionParams(path string, p wirePosition) map[string]any {
	v := documentParams(path)
	v["position"] = p
	return v
}
func decodeRaw(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return errors.Join(failure("server_error", "invalid language server response"), err)
	}
	return nil
}

func excludedDirectory(name string) bool {
	switch name {
	case ".git", "node_modules", ".venv", "venv", "vendor", "target", ".cache", "__pycache__":
		return true
	}
	return false
}

// Workspace discovery only tests whether this configured scope contains a
// matching source file. It neither searches for arbitrary project roots nor
// starts a server for unrelated languages. A bounded/incomplete walk remains a
// candidate so limits cannot silently suppress a potentially relevant server.
func (m *Manager) hasLanguageFiles(ctx context.Context, cfg ServerConfig, scope string) (bool, error) {
	found, count := false, 0
	err := filepath.WalkDir(scope, func(path string, d fs.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if err != nil || count > m.config.MaxWorkspaceFiles {
			found = true
			return fs.SkipAll
		}
		if d.IsDir() {
			if path != scope && excludedDirectory(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if language(cfg, path) != "" {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found, err
}
