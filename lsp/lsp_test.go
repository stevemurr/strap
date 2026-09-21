package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func put(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
}
func manager(t *testing.T, c Config) *Manager {
	t.Helper()
	m, err := New(c, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return m
}

func TestPositionEncodings(t *testing.T) {
	text := "\tA😀é界\r\nnext\n"
	for _, encoding := range []string{"utf-8", "utf-16", "utf-32"} {
		for _, p := range []Position{{1, 1}, {1, 3}, {1, 4}, {1, 7}, {2, 2}, {3, 1}} {
			wire, err := toWire(text, p, encoding)
			if err != nil {
				t.Fatal(err)
			}
			got, err := fromWire(text, wire, encoding)
			if err != nil || got != p {
				t.Fatalf("%s: %v -> %v -> %v (%v)", encoding, p, wire, got, err)
			}
		}
	}
	if _, err := fromWire(text, wirePosition{0, 3}, "utf-16"); err == nil {
		t.Fatal("accepted a surrogate split")
	}
	if _, err := toWire(text, Position{1, 8}, "utf-16"); err == nil {
		t.Fatal("accepted past-end column")
	}
}

func TestGoWorkspaceMembership(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	put(t, filepath.Join(root, "go.work"), "go 1.24.0\nuse ./member\n")
	put(t, filepath.Join(root, "member/go.mod"), "module example.com/member\ngo 1.24.0\n")
	put(t, filepath.Join(root, "other/go.mod"), "module example.com/other\ngo 1.24.0\n")
	for _, entry := range []struct{ path, want string }{{"member", root}, {"other", filepath.Join(root, "other")}, {"", root}} {
		got, err := goRoot(context.Background(), filepath.Join(root, entry.path))
		if err != nil || got != entry.want {
			t.Fatalf("%s: %s %v", entry.path, got, err)
		}
	}
}

func TestConfigurationAndLazyStatus(t *testing.T) {
	c := GoConfig()
	c.Dir = t.TempDir()
	c.Servers[0].Command = []string{"strap-missing-language-server"}
	m := manager(t, c)
	c.Servers[0].Languages[".go"] = "wrong"
	c.Servers[0].Command[0] = "wrong"
	status, err := m.Status(context.Background(), "")
	if err != nil || len(status.Servers) != 1 || status.Servers[0].State != "configured" {
		t.Fatalf("%+v %v", status, err)
	}
	if len(m.instances) != 0 {
		t.Fatal("status launched a server")
	}
	if m.Config().Servers[0].Languages[".go"] != "go" {
		t.Fatal("configuration alias")
	}
	put(t, filepath.Join(c.Dir, "go.mod"), "module example.com/fixture\ngo 1.24.0\n")
	put(t, filepath.Join(c.Dir, "f.go"), "package fixture\n")
	_, err = m.Outline(context.Background(), OutlineQuery{Path: "f.go"})
	var serviceError *Error
	if !errors.As(err, &serviceError) || serviceError.Kind != "server_unavailable" {
		t.Fatalf("missing server: %v", err)
	}
}

func TestBoundedCodec(t *testing.T) {
	for _, input := range []string{"Content-Length: 9000\r\n\r\n", "Content-Length: 2\r\nContent-Length: 2\r\n\r\n{}", "Content-Length: 3\r\n\r\n{}x", strings.Repeat("x", 20000)} {
		var v any
		if err := (boundedCodec{100}).ReadObject(bufio.NewReader(strings.NewReader(input)), &v); err == nil {
			t.Fatal("accepted invalid frame")
		}
	}
	r := bufio.NewReader(strings.NewReader("Content-Length: 3\r\n\r\n{} Content-Length: 2\r\n\r\n{}"))
	var v any
	for range 2 {
		if err := (boundedCodec{100}).ReadObject(r, &v); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRetainedPagesAndStaleMetadata(t *testing.T) {
	c := GoConfig()
	c.Dir = t.TempDir()
	m := manager(t, c)
	items := make([]Item, 3)
	for i := range items {
		items[i] = Item{Path: "f.go", Name: strings.Repeat("x", 50)}
	}
	p, err := m.firstPage("query", items, Metadata{Freshness: "synchronized"}, false, 1)
	if err != nil || p.NextCursor == "" {
		t.Fatal(p, err)
	}
	m.epoch.Add(1)
	next, err := m.continuePage("query", p.NextCursor, 1)
	if err != nil || next.Metadata.Freshness != "stale" || len(next.Items) != 1 {
		t.Fatal(next, err)
	}
	if _, err = m.continuePage("other", p.NextCursor, 1); err == nil {
		t.Fatal("accepted changed query")
	}
	b, _ := json.Marshal(next)
	if len(b) > outputBytes {
		t.Fatal("output bound exceeded")
	}
}

func TestDiagnosticReplacement(t *testing.T) {
	c := &client{diagnostics: map[string]diagnosticSet{}, diagnosticLimit: 1024}
	v1, v2 := 1, 2
	c.storeDiagnostics("file:///f", diagnosticSet{Version: &v2, Items: []wireDiagnostic{{Message: "current"}}})
	c.storeDiagnostics("file:///f", diagnosticSet{Version: &v1, Items: []wireDiagnostic{{Message: "old"}}})
	if c.diagnostics["file:///f"].Items[0].Message != "current" {
		t.Fatal("old publication replaced new")
	}
	c.storeDiagnostics("file:///f", diagnosticSet{Version: &v2, Items: []wireDiagnostic{}})
	if len(c.diagnostics["file:///f"].Items) != 0 {
		t.Fatal("empty set didn't clear")
	}
	if c.diagnosticBytes != 2 {
		t.Fatalf("cache accounting: %d", c.diagnosticBytes)
	}
}

func TestIndependentPushAndPullDiagnostics(t *testing.T) {
	c := &client{diagnostics: map[string]diagnosticSet{}, diagnosticLimit: 4096}
	uri := "file:///lib.rs"
	v1, v2 := 1, 2
	compiler := wireDiagnostic{Source: "rustc", Message: "compiler error"}
	analyzer := wireDiagnostic{Source: "rust-analyzer", Message: "analyzer error"}
	c.storeDiagnostics(uri, diagnosticSet{Version: &v1, Items: []wireDiagnostic{compiler}})
	c.storePulledDiagnostics(uri, diagnosticSet{Version: &v1, Items: []wireDiagnostic{}})
	sets, _ := c.diagnosticSnapshot()
	if len(sets[uri].Items) != 1 || sets[uri].Items[0].Source != "rustc" || sets[uri].Version == nil {
		t.Fatal("empty pull erased compiler diagnostics", sets[uri])
	}
	c.storePulledDiagnostics(uri, diagnosticSet{Version: &v1, Items: []wireDiagnostic{analyzer}})
	sets, _ = c.diagnosticSnapshot()
	if len(sets[uri].Items) != 2 {
		t.Fatal("lost independent producer", sets[uri])
	}
	c.storeDiagnostics(uri, diagnosticSet{Version: &v2, Items: []wireDiagnostic{}})
	sets, _ = c.diagnosticSnapshot()
	if len(sets[uri].Items) != 1 || sets[uri].Version != nil {
		t.Fatal("mixed document versions must not claim freshness", sets[uri])
	}
	c.storePulledDiagnostics(uri, diagnosticSet{Version: &v2, Items: []wireDiagnostic{}})
	sets, _ = c.diagnosticSnapshot()
	if len(sets[uri].Items) != 0 || sets[uri].Version == nil || c.diagnosticBytes != 4 {
		t.Fatal("independent clears did not settle", sets[uri], c.diagnosticBytes)
	}
	c.diagnosticRevision.Add(1)
	sets, _ = c.diagnosticSnapshot()
	if sets[uri].Version != nil {
		t.Fatal("server refresh did not invalidate old pull")
	}
}

func TestReadinessWarningsDoNotStickAfterEmptyReads(t *testing.T) {
	m := &Manager{}
	s := &instance{client: &client{startupIncomplete: true, progress: map[string]bool{}}}
	var first, later, active Metadata
	m.addReadiness(s, &first)
	m.addReadiness(s, &later)
	s.client.progress["indexing"] = true
	m.addReadiness(s, &active)
	if !first.Partial || later.Partial || !active.Partial {
		t.Fatalf("readiness warnings: first=%+v later=%+v active=%+v", first, later, active)
	}
}

func TestWatchRegistrations(t *testing.T) {
	c := &client{registrations: map[string][]fileWatcher{}}
	var regs []registration
	if err := json.Unmarshal([]byte(`[{"id":"go","method":"workspace/didChangeWatchedFiles","registerOptions":{"watchers":[{"globPattern":"**/*.{go,mod}","kind":3}]}}]`), &regs); err != nil {
		t.Fatal(err)
	}
	if err := c.register(regs); err != nil {
		t.Fatal(err)
	}
	changes := []map[string]any{
		{"uri": "file:///a.go", "type": 1}, {"uri": "file:///a/b.go", "type": 2}, {"uri": "file:///a/b.go", "type": 3}, {"uri": "file:///a/b.ts", "type": 2}, {"uri": "file:///go.mod", "type": 1},
	}
	if got := c.watchedChanges(changes); len(got) != 3 {
		t.Fatalf("glob/kind mismatch: %+v", got)
	}
	delete(c.registrations, "go")
	if len(c.watchedChanges(changes)) != 0 {
		t.Fatal("unregistered watcher still active")
	}
}

func TestPagesDoNotAliasAndDetectUnwatchedChanges(t *testing.T) {
	c := GoConfig()
	c.Dir = t.TempDir()
	m := manager(t, c)
	path := filepath.Join(c.Dir, "f.go")
	put(t, path, "Alpha\n")
	r := &Range{Position{1, 1}, Position{1, 6}}
	items := []Item{{Path: "f.go", SHA256: digest([]byte("Alpha\n")), Selection: r}, {Path: "f.go", SHA256: digest([]byte("Alpha\n")), Selection: r}}
	page, err := m.firstPage("query", items, Metadata{Freshness: "synchronized", Sources: []Source{{Server: "original"}}}, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	page.Items[0].Selection.Start.Line = 99
	page.Metadata.Sources[0].Server = "changed"
	put(t, path, "Other\n") // No server/watch exists; continuation must check content.
	next, err := m.continuePage("query", page.NextCursor, 1)
	if err != nil || next.Items[0].Selection.Start.Line != 1 || next.Metadata.Sources[0].Server != "original" || next.Metadata.Freshness != "stale" {
		t.Fatalf("immutable page: %+v %v", next, err)
	}
}

func TestGoExplicitWorkspacePolicy(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	put(t, filepath.Join(root, "go.work"), "go 1.24.0\nuse ./member\n")
	put(t, filepath.Join(root, "member/go.mod"), "module example.com/member\ngo 1.24.0\n")
	member := filepath.Join(root, "member")
	if got, err := goRootWithWorkspace(context.Background(), member, "off"); err != nil || got != member {
		t.Fatal(got, err)
	}
	if got, err := goRootWithWorkspace(context.Background(), member, filepath.Join(root, "go.work")); err != nil || got != root {
		t.Fatal(got, err)
	}
	put(t, filepath.Join(root, "other/go.mod"), "module example.com/other\ngo 1.24.0\n")
	if _, err := goRootWithWorkspace(context.Background(), filepath.Join(root, "other"), filepath.Join(root, "go.work")); err == nil {
		t.Fatal("ignored configured workspace membership")
	}
}
