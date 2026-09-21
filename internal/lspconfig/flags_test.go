package lspconfig

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestFlags(t *testing.T) {
	file := filepath.Join(t.TempDir(), "lsp.json")
	if err := os.WriteFile(file, []byte(`{"servers":[{"id":"ts","command":["typescript-language-server","--stdio"],"languages":{".ts":"typescript"}}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		id   string
		bad  bool
	}{
		{nil, "gopls", false}, {[]string{"-lsp"}, "gopls", false}, {[]string{"-lsp-config", file}, "ts", false},
		{[]string{"-lsp=false"}, "", false},
		{[]string{"-lsp=false", "-lsp-config", "missing"}, "", false}, {[]string{"-lsp-config", "missing"}, "", true},
	} {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		o := Flags(fs)
		if err := fs.Parse(tc.args); err != nil {
			t.Fatal(err)
		}
		cfg, err := o.Resolve()
		if (err != nil) != tc.bad {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if tc.id == "" {
			if cfg != nil {
				t.Fatalf("unexpected config: %+v", cfg)
			}
		} else if cfg == nil || cfg.Servers[0].ID != tc.id {
			t.Fatalf("config: %+v", cfg)
		}
		if tc.id == "gopls" && len(cfg.Servers) != 5 {
			t.Fatalf("-lsp must enable all five server presets: %+v", cfg)
		}
		if tc.id == "ts" && len(cfg.Servers) != 1 {
			t.Fatal("explicit configuration must replace the presets")
		}
	}
}
