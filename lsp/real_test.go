package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type languageFixture struct {
	name, filename, text, broken string
	config                       Config
	files                        map[string]string
	required                     []string
}

func languageFixtures() []languageFixture {
	goText := "package fixture\nfunc Alpha() int { return 1 }\nfunc Beta() int { return Alpha() }\n"
	tsText := "export function Alpha(): number { return 1; }\nexport function Beta(): number { return Alpha(); }\n"
	jsText := "// @ts-check\n/** @returns {number} */\nexport function Alpha() { return 1; }\nexport function Beta() { return Alpha(); }\n"
	rustText := "#![allow(non_snake_case)]\npub fn Alpha() -> i32 { 1 }\npub fn Beta() -> i32 { Alpha() }\n"
	pythonText := "def Alpha() -> int:\n    return 1\n\ndef Beta() -> int:\n    return Alpha()\n"
	bashText := "#!/usr/bin/env bash\n# Print a value.\nAlpha() { printf '%s\\n' \"ok\"; }\nBeta() { Alpha; }\nBeta\n"
	return []languageFixture{
		{"go", "fixture.go", goText, strings.Replace(goText, "return 1", `return "bad"`, 1), GoConfig(), map[string]string{"go.mod": "module example.com/fixture\ngo 1.24.0\n"}, nil},
		{"typescript", "fixture.ts", tsText, strings.Replace(tsText, "return 1", `return "bad"`, 1), TypeScriptConfig(), map[string]string{"tsconfig.json": `{"compilerOptions":{"strict":true,"noEmit":true},"include":["*.ts"]}`}, nil},
		{"javascript", "fixture.js", jsText, strings.Replace(jsText, "return 1", `return "bad"`, 1), TypeScriptConfig(), map[string]string{"jsconfig.json": `{"compilerOptions":{"checkJs":true,"noEmit":true},"include":["*.js"]}`}, nil},
		{"rust", "src/lib.rs", rustText, strings.Replace(rustText, "{ 1 }", `{ "bad" }`, 1), RustConfig(), map[string]string{"Cargo.toml": "[package]\nname = \"strap_lsp_fixture\"\nversion = \"0.1.0\"\nedition = \"2021\"\n"}, []string{"cargo"}},
		{"python", "fixture.py", pythonText, strings.Replace(pythonText, "return 1", `return "bad"`, 1), PythonConfig(), map[string]string{"pyrightconfig.json": `{"typeCheckingMode":"standard","include":["fixture.py"]}`}, []string{"python3"}},
		{"bash", "fixture.sh", bashText, strings.Replace(bashText, `"ok"`, `"$missing"`, 1), BashConfig(), nil, []string{"shellcheck"}},
	}
}

// All cases run through the shipped presets. Opt-in tests fail for missing
// dependencies instead of silently passing with a skipped language.
func TestRealLanguageServers(t *testing.T) {
	if os.Getenv("STRAP_LSP_REAL") != "1" {
		t.Skip("set STRAP_LSP_REAL=1 for installed language servers")
	}
	for _, fixture := range languageFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			for _, binary := range append(fixture.required, fixture.config.Servers[0].Command[0]) {
				if _, err := exec.LookPath(binary); err != nil {
					t.Fatal(err)
				}
			}
			root := t.TempDir()
			for path, text := range fixture.files {
				put(t, filepath.Join(root, path), text)
			}
			path := filepath.Join(root, fixture.filename)
			put(t, path, fixture.text)
			c := fixture.config
			c.Dir = root
			c.DiagnosticTimeoutMS = 5000
			c.RequestTimeoutMS = 30000
			m := manager(t, c)
			t.Cleanup(func() {
				if t.Failed() {
					for _, s := range m.instances {
						if s.client != nil {
							logs, _ := s.client.stderr.Snapshot()
							t.Logf("server stderr: %s", logs)
						}
					}
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			outline, err := m.Outline(ctx, OutlineQuery{Path: fixture.filename})
			if err != nil {
				t.Fatal(err)
			}
			var alpha Item
			for _, item := range outline.Items {
				if item.Name == "Alpha" {
					alpha = item
				}
			}
			if alpha.Ref == "" {
				t.Fatalf("missing Alpha: %+v", outline)
			}
			refs, err := m.References(ctx, ReferenceQuery{Target: Target{Ref: alpha.Ref}, IncludeDeclaration: true})
			if err != nil || len(refs.Items) < 2 {
				t.Fatalf("references: %+v %v", refs, err)
			}
			usage := refs.Items[len(refs.Items)-1]
			inspect, err := m.Inspect(ctx, InspectQuery{Target: Target{Ref: usage.Ref}, IncludeSource: true})
			if err != nil || !strings.Contains(inspect.Documentation, "Alpha") {
				t.Fatalf("inspect: %+v %v", inspect, err)
			}
			anchored, err := m.Inspect(ctx, InspectQuery{Target: Target{Path: usage.Path, Line: usage.Selection.Start.Line, Symbol: "Alpha"}, IncludeSource: true})
			if err != nil || !strings.Contains(anchored.Documentation, "Alpha") {
				t.Fatalf("symbol target: %+v %v", anchored, err)
			}
			def, err := m.Navigate(ctx, NavigateQuery{Target: Target{Ref: refs.Items[len(refs.Items)-1].Ref}, Relation: "definition"})
			if err != nil || len(def.Items) != 1 {
				t.Fatalf("definition: %+v %v", def, err)
			}
			symbols, err := m.Symbols(ctx, SymbolQuery{Query: "Alpha"})
			if err != nil || len(symbols.Items) == 0 {
				t.Fatalf("symbols: %+v %v", symbols, err)
			}
			put(t, path, fixture.broken)
			m.Changed(path)
			if _, err = m.Inspect(ctx, InspectQuery{Target: Target{Ref: alpha.Ref}}); err == nil || !strings.Contains(err.Error(), "stale_reference") {
				t.Fatalf("stale reference: %v", err)
			}
			awaitFixtureDiagnostics(t, ctx, m, fixture.filename, false)
			put(t, path, fixture.text)
			m.Changed(path)
			awaitFixtureDiagnostics(t, ctx, m, fixture.filename, true)
			status, err := m.Status(ctx, "")
			if err != nil || len(status.Servers) != 1 || status.Servers[0].State != "running" {
				t.Fatal(status, err)
			}
			t.Logf("verified %s: %s", fixture.name, status.Servers[0].Version)
		})
	}
}

// Analysis is asynchronous: even a full pull can precede a later compiler
// publication or diagnostic refresh. Verify eventual appearance AND clearing,
// including versionless peers, without requiring a project convergence barrier.
func awaitFixtureDiagnostics(t *testing.T, ctx context.Context, m *Manager, path string, clean bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		page, err := m.Diagnostics(ctx, DiagnosticQuery{Paths: []string{path}})
		if err != nil {
			t.Fatalf("diagnostics (clean=%v): %+v %v", clean, page, err)
		}
		if len(page.Metadata.Checks) == 1 && len(page.Metadata.Issues) == 0 && (len(page.Items) == 0) == clean {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("diagnostics did not settle (clean=%v): %+v", clean, page)
		case <-time.After(100 * time.Millisecond):
		}
	}
}
