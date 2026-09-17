package modelcatalog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stevemurr/strap/provider"
)

// A profile constrains tools by name, so a renamed or mistyped tool would fail
// silently: the request would simply carry no constraint. Resolving every
// bundled profile against a real request keeps the names honest, and checks
// that the constraint reaches exactly the tool asked for.
func TestBundledStrictToolsReachTheirToolAndNoOther(t *testing.T) {
	for _, name := range []string{"qwen3.6", "qwen3.6-nothink"} {
		t.Run(name, func(t *testing.T) {
			model, _, err := Resolve("models.json", name, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			want := model.Generation.StrictTools
			if len(want) == 0 {
				t.Skip("profile constrains no tool")
			}
			marked := map[string]bool{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Tools []struct {
						Function struct {
							Name   string `json:"name"`
							Strict *bool  `json:"strict"`
						} `json:"function"`
					} `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				for _, tool := range body.Tools {
					if tool.Function.Strict != nil && *tool.Function.Strict {
						marked[tool.Function.Name] = true
					}
				}
				w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
			}))
			defer server.Close()
			model.BaseURL = server.URL
			p, err := model.NewProvider(server.Client())
			if err != nil {
				t.Fatal(err)
			}
			request := provider.Request{Tools: []provider.ToolDefinition{
				{Name: "create_plan", Parameters: json.RawMessage(`{"type":"object"}`)},
				{Name: "report_work_progress", Parameters: json.RawMessage(`{"type":"object"}`)},
				{Name: "shell", Parameters: json.RawMessage(`{"type":"object"}`)},
			}}
			if _, err := p.Submit(t.Context(), request, nil); err != nil {
				t.Fatal(err)
			}
			for _, tool := range want {
				if !marked[tool] {
					t.Errorf("%s names %s, which no advertised tool matched", name, tool)
				}
				delete(marked, tool)
			}
			if len(marked) != 0 {
				t.Errorf("constrained tools nobody asked for: %v", marked)
			}
		})
	}
}
