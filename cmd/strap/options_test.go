package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/internal/modelcatalog"
)

func writeCatalog(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSavedProfileAndFlagPrecedence(t *testing.T) {
	path := writeCatalog(t, `{"default":"custom","models":{"custom":{
		"base_url":"http://localhost:1234", "model":"saved-alias", "timeout":"2m",
		"generation":{"temperature":0.7,"top_p":0.8,"enable_thinking":true,"force_nonempty_content":true,"reasoning_effort":"low"}
	}}}`)
	for _, args := range [][]string{
		{"-config", path, "-temperature", "0", "-thinking=false", "-force-nonempty-content=false", "-timeout", "3m", "-reasoning-effort", "medium"},
		{"-reasoning-effort", "medium", "-temperature", "0", "-thinking=false", "-force-nonempty-content=false", "-timeout", "3m", "-config", path},
	} {
		o, err := parseOptions(args, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		m := o.config.Model
		if m.Model != "saved-alias" || m.BaseURL != "http://localhost:1234" || m.Timeout != 3*time.Minute {
			t.Fatalf("unexpected model: %+v", m)
		}
		g := m.Generation
		if *g.Temperature != 0 || *g.TopP != 0.8 || *g.EnableThinking || *g.ForceNonemptyContent || *g.ReasoningEffort != "medium" {
			t.Fatalf("flags failed to override saved values: %+v", g)
		}
	}
	o, err := parseOptions([]string{"-config", path}, io.Discard)
	if err != nil || o.config.Model.Timeout != 2*time.Minute || *o.config.Model.Generation.Temperature != 0.7 {
		t.Fatalf("saved defaults lost: %+v, %v", o, err)
	}
}

func TestCatalogDiscovery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	// The bundled catalog must resolve without a personal catalog; which
	// profile it selects is configuration, not a contract.
	m, _, err := modelcatalog.Resolve("", "", time.Hour)
	if err != nil || m.Model == "" || m.BaseURL == "" {
		t.Fatalf("bundled fallback: %+v, %v", m, err)
	}
	for _, dir := range []string{filepath.Join(home, ".config"), t.TempDir()} {
		if dir != filepath.Join(home, ".config") {
			t.Setenv("XDG_CONFIG_HOME", dir)
		}
		if err := os.MkdirAll(filepath.Join(dir, "strap"), 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "strap", "models.json")
		if err := os.WriteFile(path, []byte(`{"default":"mine","models":{"mine":{"model":"personal"}}}`), 0600); err != nil {
			t.Fatal(err)
		}
		m, _, err := modelcatalog.Resolve("", "", time.Hour)
		if err != nil || m.Model != "personal" {
			t.Fatalf("personal catalog: %+v, %v", m, err)
		}
	}
	if _, _, err := modelcatalog.Resolve(filepath.Join(home, "missing.json"), "", time.Hour); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("explicit missing config must fail: %v", err)
	}
}

func TestCatalogErrorsAndHelp(t *testing.T) {
	for _, body := range []string{
		`{`, `null`, `{}`, `{"default":"missing","models":{}}`,
		`{"default":"x","models":{"x":{"temperatur":1}}}`,
		`{"default":"x","models":{"x":{"generation":{"temperatur":1}}}}`,
		`{"default":"x","models":{"x":{"timeout":"bad"}}}`,
		`{"default":"x","models":{"x":{}}} {}`,
	} {
		path := writeCatalog(t, body)
		if _, err := parseOptions([]string{"-config", path}, io.Discard); err == nil {
			t.Errorf("accepted %s", body)
		}
		var out bytes.Buffer
		if _, err := parseOptions([]string{"-config", path, "-help"}, &out); !errors.Is(err, flag.ErrHelp) || !strings.Contains(out.String(), "-profile") {
			t.Fatalf("help must work with invalid config: %v, %s", err, out.String())
		}
	}
}

func TestToolAndRecordingOptions(t *testing.T) {
	dir := t.TempDir()
	args := []string{"-config", catalogPath, "-C", dir, "-record", "session.jsonl", "-listen", "127.0.0.1:0", "-wkrender", "wk", "-agent-browser", "ab", "-browser-executable", "chrome"}
	o, err := parseOptions(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	w := o.config.Web
	if o.config.Dir != dir || o.config.Events.JSONLPath != "session.jsonl" || o.listen != "127.0.0.1:0" || w.WKRenderPath != "wk" || w.AgentBrowserPath != "ab" || w.BrowserExecutablePath != "chrome" {
		t.Fatalf("options lost: %+v", o)
	}
	o, err = parseOptions(append(args, "-web=false"), io.Discard)
	if err != nil || o.config.Web != nil {
		t.Fatalf("web disable: %+v, %v", o, err)
	}
}
