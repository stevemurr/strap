package lsp

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPresetRouting(t *testing.T) {
	root := t.TempDir()
	c := DefaultConfig()
	c.Dir = root
	put(t, filepath.Join(root, "go.mod"), "module example.com/fixture\ngo 1.24.0\n")
	put(t, filepath.Join(root, "Cargo.toml"), "[package]\nname=\"fixture\"\nversion=\"0.1.0\"\n")
	m := manager(t, c)
	for _, tc := range []struct{ path, server, language string }{
		{"main.go", "gopls", "go"}, {"src/lib.rs", "rust-analyzer", "rust"},
		{"main.py", "pyright", "python"}, {"main.pyi", "pyright", "python"}, {"gui.pyw", "pyright", "python"},
		{"index.js", "typescript-language-server", "javascript"}, {"index.mjs", "typescript-language-server", "javascript"}, {"index.cjs", "typescript-language-server", "javascript"},
		{"view.jsx", "typescript-language-server", "javascriptreact"}, {"view.tsx", "typescript-language-server", "typescriptreact"},
		{"index.ts", "typescript-language-server", "typescript"}, {"index.mts", "typescript-language-server", "typescript"}, {"index.cts", "typescript-language-server", "typescript"},
		{"run.sh", "bash-language-server", "shellscript"}, {"run.bash", "bash-language-server", "shellscript"}, {".bashrc", "bash-language-server", "shellscript"}, {".bash_profile", "bash-language-server", "shellscript"},
	} {
		put(t, filepath.Join(root, tc.path), "")
		path, err := m.path(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		s, lang, err := m.forFile(context.Background(), path)
		if err != nil {
			t.Fatal(tc.path, err)
		}
		if s.config.ID != tc.server || lang != tc.language {
			t.Fatalf("%s: %s/%s", tc.path, s.config.ID, lang)
		}
		if s.client != nil {
			t.Fatal("routing eagerly started a server")
		}
	}
	if len(m.instances) != 5 {
		t.Fatalf("expected one configuration per server, got %d", len(m.instances))
	}
	fresh := DefaultConfig()
	c.Servers[1].Languages[".rs"] = "changed"
	c.Servers[2].Settings[0] = '!'
	if fresh.Servers[1].Languages[".rs"] != "rust" || fresh.Servers[2].Settings[0] != '{' {
		t.Fatal("presets share mutable configuration")
	}
}

func TestPresetRootSelection(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	for _, tc := range []struct {
		config       Config
		path, marker string
	}{
		{PythonConfig(), "project/src/main.py", "pyproject.toml"},
		{TypeScriptConfig(), "project/src/index.js", "jsconfig.json"},
		{RustConfig(), "project/src/lib.rs", "Cargo.toml"},
		{BashConfig(), "project/scripts/run.sh", ".git"},
	} {
		t.Run(tc.config.Servers[0].ID, func(t *testing.T) {
			put(t, filepath.Join(root, tc.path), "")
			put(t, filepath.Join(root, "project", tc.marker), "")
			c := tc.config
			c.Dir = root
			m := manager(t, c)
			path, _ := m.path(tc.path)
			s, _, err := m.forFile(context.Background(), path)
			if err != nil || s.spec.Root != filepath.Join(root, "project") {
				t.Fatalf("root: %+v %v", s, err)
			}
		})
	}
	for _, config := range []Config{PythonConfig(), TypeScriptConfig(), BashConfig()} {
		c := config
		c.Dir = t.TempDir()
		m := manager(t, c)
		filename := "single.py"
		if c.Servers[0].ID == "typescript-language-server" {
			filename = "single.js"
		}
		if c.Servers[0].ID == "bash-language-server" {
			filename = "single.sh"
		}
		put(t, filepath.Join(c.Dir, filename), "")
		path, _ := m.path(filename)
		s, _, err := m.forFile(context.Background(), path)
		if err != nil || s.spec.Root != m.config.Dir {
			t.Fatalf("standalone root: %+v %v", s, err)
		}
	}
}

func TestWorkspaceQueriesOnlyStartApplicableServers(t *testing.T) {
	// A single source-language scope must not fail because unrelated preset
	// binaries are missing, or spend instance slots launching those servers.
	fixture, _ := fakeConfig(t, "")
	c := DefaultConfig()
	c.Dir = fixture.Dir
	c.Servers[0] = fixture.Servers[0]
	for i := 1; i < len(c.Servers); i++ {
		c.Servers[i].Command = []string{"missing-unrelated-server"}
	}
	m := manager(t, c)
	// The fixture deliberately lacks workspace symbols; selection still resolves
	// only its Go configuration and never attempts another server.
	_, _ = m.Symbols(context.Background(), SymbolQuery{Query: "Alpha"})
	if len(m.instances) != 1 {
		t.Fatalf("unrelated instances: %d", len(m.instances))
	}
	for _, s := range m.instances {
		if s.config.ID != "fixture" || s.client == nil {
			t.Fatalf("wrong selected server: %+v", s)
		}
	}
}
