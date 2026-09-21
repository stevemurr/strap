package tool

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stevemurr/strap/lsp"
)

type languageSpy struct {
	inspected []lsp.InspectQuery
	navigated []lsp.NavigateQuery
}

func (*languageSpy) Status(context.Context, string) (lsp.Status, error) { return lsp.Status{}, nil }
func (*languageSpy) Symbols(context.Context, lsp.SymbolQuery) (lsp.Page, error) {
	return lsp.Page{}, nil
}
func (*languageSpy) Outline(context.Context, lsp.OutlineQuery) (lsp.Page, error) {
	return lsp.Page{}, nil
}
func (s *languageSpy) Inspect(_ context.Context, q lsp.InspectQuery) (lsp.Inspection, error) {
	s.inspected = append(s.inspected, q)
	return lsp.Inspection{}, nil
}
func (s *languageSpy) Navigate(_ context.Context, q lsp.NavigateQuery) (lsp.Page, error) {
	s.navigated = append(s.navigated, q)
	return lsp.Page{}, nil
}
func (*languageSpy) References(context.Context, lsp.ReferenceQuery) (lsp.Page, error) {
	return lsp.Page{}, nil
}
func (*languageSpy) Diagnostics(context.Context, lsp.DiagnosticQuery) (lsp.Page, error) {
	return lsp.Page{}, nil
}

func TestLanguageContracts(t *testing.T) {
	spy := &languageSpy{}
	tools, err := LSPTools(spy)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Tool{}
	for _, tool := range tools {
		if err := ValidateTool(tool); err != nil {
			t.Fatal(err)
		}
		byName[tool.Definition().Name] = tool
	}
	if len(byName) != 7 {
		t.Fatal(byName)
	}
	for _, tc := range []struct {
		name, input string
		valid       bool
	}{
		{"lsp_status", `{"path":null}`, true},
		{"lsp_outline", `{"path":"a.go","depth":null,"limit":null,"cursor":null}`, true},
		{"lsp_inspect", `{"target_kind":"reference","ref":"loc_1","include_source":null}`, true},
		{"lsp_inspect", `{"target_kind":"symbol","path":"a.go","line":1,"symbol":"Alpha","context":null,"include_source":true}`, true},
		{"lsp_inspect", `{"target_kind":"reference","ref":"loc_1","path":"a.go","include_source":null}`, false},
		{"lsp_inspect", `{"target_kind":"position","path":"a.go","line":0,"column":2,"expected_text":null,"include_source":true}`, false},
		{"lsp_inspect", `{"target_kind":"position","path":"a.go","line":1,"column":6,"expected_text":"Alpha","include_source":true}`, false},
		{"lsp_inspect", `{"target_kind":"position","path":"a.go","line":1,"column":2,"include_source":true}`, false},
		{"lsp_inspect", `{"target_kind":"symbol","path":"a.go","line":0,"symbol":"Alpha","context":null,"include_source":true}`, false},
		{"lsp_inspect", `{"target_kind":"symbol","path":"a.go","line":1,"symbol":"Alpha","include_source":true}`, false},
		{"lsp_inspect", `{"target_kind":"symbol","path":"a.go","line":1,"symbol":"Alpha","context":null,"column":2,"include_source":true}`, false},
		{"lsp_navigate", `{"target_kind":"reference","ref":"loc_1","relation":"definition","limit":null,"cursor":null}`, true},
		{"lsp_navigate", `{"target_kind":"reference","ref":"loc_1","relation":"rename","limit":null,"cursor":null}`, false},
		{"lsp_diagnostics", `{"paths":null,"limit":null,"cursor":null}`, true},
		{"lsp_diagnostics", `{"paths":[],"limit":null,"cursor":null}`, false},
		{"lsp_references", `{"target_kind":"reference","ref":"loc_1","include_declaration":true,"limit":201,"cursor":null}`, false},
	} {
		raw := json.RawMessage(`{"input":` + tc.input + `}`)
		tool := byName[tc.name]
		validated := ValidateArguments(tool, raw)
		_, err := tool.Call(context.Background(), Call{Arguments: raw})
		if (validated == nil) != tc.valid || (err == nil) != tc.valid {
			t.Fatalf("%s %s: validator=%v call=%v", tc.name, tc.input, validated, err)
		}
	}
	if len(spy.inspected) != 2 || spy.inspected[0].Target.Ref != "loc_1" || spy.inspected[1].Target.Symbol != "Alpha" || !spy.inspected[1].IncludeSource || len(spy.navigated) != 1 {
		t.Fatalf("dispatch: %+v", spy)
	}
}

func TestMutationNotificationsFollowCommit(t *testing.T) {
	var files *Files
	var observed []string
	var err error
	files, err = NewFiles(FilesConfig{Dir: t.TempDir(), OnChange: func(path string) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if err := files.lock(ctx); err != nil {
			t.Error("notification held file lock", err)
			return
		}
		files.unlock()
		observed = append(observed, path)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = files.write(context.Background(), Call{}, writeArgs{Path: "a.go", Content: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err = files.edit(context.Background(), Call{}, editArgs{Path: "a.go", Old: "missing", New: "x"}); err == nil {
		t.Fatal("expected failed edit")
	}
	if len(observed) != 1 {
		t.Fatal("failed edit notified", observed)
	}
	if _, err = files.edit(context.Background(), Call{}, editArgs{Path: "a.go", Old: "first", New: "second"}); err != nil {
		t.Fatal(err)
	}
	if len(observed) != 2 {
		t.Fatal(observed)
	}
	runs := 0
	shell, err := NewShell(ShellConfig{Dir: t.TempDir(), AfterRun: func() { runs++ }})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = shell.Call(context.Background(), Call{Arguments: json.RawMessage(`{"input":{"command":"exit 2","timeout_ms":null}}`)})
	if runs != 1 {
		t.Fatal("failed command did not notify")
	}
	_, _ = shell.Call(context.Background(), Call{Arguments: json.RawMessage(`{"input":{"command":"sleep 10","timeout_ms":10}}`)})
	if runs != 2 {
		t.Fatal("timed out command did not notify")
	}
}
