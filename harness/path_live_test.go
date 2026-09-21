package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/lsp"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/vllm"
	"github.com/stevemurr/strap/tool"
)

// Candidate retained for evaluation; this wording was not promoted to production.
const pathCandidateGrounding = "Local file tools and shell commands start in the same session working directory. Prefer workspace-relative paths such as README.md instead of reconstructing the absolute workspace prefix. Copy existing file paths from the user or observed results, preserving spaces and spelling. Use shell discovery when an existing path is unknown; do not infer directories from package, module, or project names. A shell cd applies only to that call and does not change other tools' base directory. Preserve observed paths verbatim in plans and assignments; distinguish a package name from a file path, and resolve conflicting assignment paths against source evidence. A directory must be listed with the shell, not read as a file. Inspect shell output for discovery errors even when its exit code is zero. For an intentional new file, choose a path consistent with the request and observed layout; verify its parent directory and create a missing parent with the shell before writing. After a path error, use the supplied path or discover the current layout before retrying; do not invent a different root."

type pathCase struct {
	name, prompt, wantTool, wantPath string
	receipt                          bool
}
type pathAttempt struct {
	Response      provider.Response `json:"response"`
	Result        string            `json:"result,omitempty"`
	Error         string            `json:"error,omitempty"`
	Correct       bool              `json:"correct"`
	WrongExisting bool              `json:"wrong_existing"`
	ElapsedMS     int64             `json:"elapsed_ms"`
}
type pathTrial struct {
	Case     string             `json:"case"`
	Variant  string             `json:"variant"`
	Repeat   int                `json:"repeat"`
	Messages []provider.Message `json:"messages"`
	Attempts []pathAttempt      `json:"attempts"`
}

// A factorial selection/recovery probe, not a full-harness task-success benchmark.
// Frozen baseline and candidate schemas differ only in descriptions.
func TestLivePathConfigurations(t *testing.T) {
	if os.Getenv("STRAP_LIVE_PATHS") != "1" {
		t.Skip("set STRAP_LIVE_PATHS=1 and STRAP_LIVE_BASE_URL/MODEL")
	}
	base, model := os.Getenv("STRAP_LIVE_BASE_URL"), os.Getenv("STRAP_LIVE_MODEL")
	if base == "" || model == "" {
		t.Fatal("endpoint and model are required")
	}
	output := os.Getenv("STRAP_LIVE_PATH_OUTPUT")
	if output == "" {
		t.Fatal("set STRAP_LIVE_PATH_OUTPUT to retain all trials")
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "strap-eval-hard-12-later-lower-counts-2039952766", "workspace")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	dir, _ = filepath.EvalSymlinks(dir)
	fixtures := map[string]string{
		"go.mod":                "module example.com/pathfixture\n\ngo 1.24.0\n",
		"README.md":             "Path fixture contract. Implement SharedPrefix in prefix.go, package config.\n",
		"prefix.go":             "package config\nfunc SharedPrefix(keys []string) string { return \"ROOT_TARGET\" }\n",
		"config/prefix.go":      "package config\nfunc SharedPrefix(keys []string) string { return \"DECOY_TARGET\" }\n",
		"preview/emoji file.go": "package preview\n\nimport \"example.com/pathfixture/pricing\"\n\nfunc Emoji() int { _ = \"🌎\"; return pricing.RateFor(\"vip\") }\n",
		"preview/emoji/file.go": "package emoji\n\nimport \"example.com/pathfixture/legacy\"\n\nfunc Emoji() int { _ = \"🌎\"; return legacy.RateFor(\"vip\") }\n",
		"pricing/rate.go":       "package pricing\nfunc RateFor(tier string) int { return 8 }\n",
		"legacy/rate.go":        "package legacy\nfunc RateFor(tier string) int { return 99 }\n",
	}
	for path, text := range fixtures {
		p := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	local, err := localTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := lsp.GoConfig()
	cfg.Dir = dir
	manager, err := lsp.New(cfg, lsp.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	ls, err := tool.LSPTools(manager)
	if err != nil {
		t.Fatal(err)
	}
	fixtureShell, err := tool.NewShell(tool.ShellConfig{Dir: dir, Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + dir}})
	if err != nil {
		t.Fatal(err)
	}
	for i, item := range local {
		if item.Definition().Name == "shell" {
			local[i] = fixtureShell
		}
	}
	local = append(local, ls...)
	byName := map[string]tool.Tool{}
	candidate := []provider.ToolDefinition{}
	for _, item := range local {
		byName[item.Definition().Name] = item
		candidate = append(candidate, item.Definition())
	}
	raw, err := os.ReadFile("testdata/path-tools-baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	var original []provider.ToolDefinition
	if err = json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	// Load the evaluated candidate independently of production descriptions.
	candidateRaw, err := os.ReadFile("testdata/path-tools-candidate.json")
	if err != nil {
		t.Fatal(err)
	}
	var frozenCandidate []provider.ToolDefinition
	if err := json.Unmarshal(candidateRaw, &frozenCandidate); err != nil {
		t.Fatal(err)
	}
	candidateByName := map[string]provider.ToolDefinition{}
	for _, d := range frozenCandidate {
		d.Description = strings.ReplaceAll(d.Description, "WORKSPACE_ROOT", dir)
		candidateByName[d.Name] = d
	}
	for i, d := range candidate {
		v, ok := candidateByName[d.Name]
		if !ok {
			t.Fatalf("no frozen candidate for %s", d.Name)
		}
		candidate[i] = v
	}
	// Preserve the exact candidate roster/order, including tools unused by a case.
	old := map[string]provider.ToolDefinition{}
	for _, d := range original {
		d.Description = strings.ReplaceAll(d.Description, "WORKSPACE_ROOT", dir)
		old[d.Name] = d
	}
	baseline := make([]provider.ToolDefinition, len(candidate))
	for i, d := range candidate {
		v, ok := old[d.Name]
		if !ok {
			t.Fatalf("no frozen baseline for %s", d.Name)
		}
		if strings.Contains(v.Description, "/private/private/") || (strings.Contains(v.Description, "WORKSPACE_ROOT")) {
			t.Fatal("invalid baseline workspace substitution")
		}
		baseline[i] = v
	}
	cases := []pathCase{
		{"workspace_root", "Read README.md to understand this workspace's contract.", "read_file", "README.md", false},
		{"directory_discovery", "List the files in the workspace root so I can choose one to inspect.", "shell", "", false},
		{"package_is_not_directory", "Inspect prefix.go, the implementation in Go package config used by the config admin UI.", "read_file", "prefix.go", false},
		{"spaced_filename", "Read preview/emoji file.go, including the Emoji implementation.", "read_file", "preview/emoji file.go", false},
		{"lsp_spaced_filename", "Find the definition of RateFor called on line 5 of preview/emoji file.go. The exact source line is:\nfunc Emoji() int { _ = \"🌎\"; return pricing.RateFor(\"vip\") }", "lsp_navigate", "preview/emoji file.go", false},
		{"conflicting_handoff", "A worker assignment says: inspect SharedPrefix in config/prefix.go. Its context says: package config, file prefix.go at the workspace root, as shown in the preceding source receipt. Inspect the implementation identified by that receipt.", "read_file", "prefix.go", true},
		{"new_file", "Create a new file notes.txt in the workspace root containing exactly: path probe\n", "write_file", "notes.txt", false},
		{"new_parent", "We need a new file scratch/notes.txt. The scratch directory does not exist yet. Prepare its parent directory as the next filesystem step.", "shell", "scratch", false},
	}
	maxTokens, temperature, topP, topK, penalty, thinking := 4096, 1.0, 0.95, 20, 1.1, true
	generation := vllm.Generation{MaxTokens: &maxTokens, Temperature: &temperature, TopP: &topP, TopK: &topK, RepetitionPenalty: &penalty, EnableThinking: &thinking}
	client, err := vllm.New(vllm.Config{BaseURL: base, Model: model, Generation: generation})
	if err != nil {
		t.Fatal(err)
	}
	system := "You are investigating a synthetic workspace. Use exactly one tool as your next step to fulfill the request. All tool arguments are under input; every declared field is required and nullable fields may use null. Do not answer with prose instead of using a tool."
	artifact := map[string]any{"model": model, "generation": generation, "baseline_tools": baseline, "candidate_tools": candidate, "baseline_system": system, "grounding": pathCandidateGrounding, "workspace": dir, "fixtures": fixtures, "repeats": 3, "max_attempts": 3, "order": "rotate four variants by case index plus repeat; sequential requests"}
	manifest, _ := json.MarshalIndent(artifact, "", "  ")
	stamp := time.Now().UTC().Format("20060102T150405")
	if err := os.WriteFile(filepath.Join(output, "manifest-"+stamp+".json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.OpenFile(filepath.Join(output, "trials-"+stamp+".jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	variants := []string{"baseline", "descriptions", "grounding", "both"}
	for repeat := 0; repeat < 3; repeat++ {
		for ci, tc := range cases {
			for vi := 0; vi < 4; vi++ {
				variant := variants[(vi+ci+repeat)%4]
				// Reset only fixture-owned outputs; source inputs remain identical across cells.
				if err := os.RemoveAll(filepath.Join(dir, "scratch")); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(dir, "notes.txt")); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				defs := baseline
				sys := system
				if variant == "descriptions" || variant == "both" {
					defs = candidate
				}
				if variant == "grounding" || variant == "both" {
					sys += "\n" + pathCandidateGrounding
				}
				messages := []provider.Message{{Role: "system", Content: content.Text(sys)}}
				if tc.receipt {
					args := json.RawMessage(`{"input":{"path":"prefix.go","offset":1,"limit":200}}`)
					receipt, err := byName["read_file"].Call(context.Background(), tool.Call{Arguments: args})
					if err != nil {
						t.Fatal(err)
					}
					messages = append(messages, provider.Message{Role: "user", Content: content.Text("Read the workspace-root implementation prefix.go.")}, provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "observed_source", Name: "read_file", Arguments: args}}}, provider.Message{Role: "tool", ToolCallID: "observed_source", Content: receipt.Content})
				}
				messages = append(messages, provider.Message{Role: "user", Content: content.Text(tc.prompt)})
				trial := pathTrial{Case: tc.name, Variant: variant, Repeat: repeat + 1, Messages: provider.CopyMessages(messages)}
				for attempt := 0; attempt < 3; attempt++ {
					ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
					start := time.Now()
					response, err := client.Submit(ctx, provider.Request{Tools: defs, Messages: messages}, nil)
					a := pathAttempt{Response: response, ElapsedMS: time.Since(start).Milliseconds()}
					if err != nil {
						a.Error = "provider: " + err.Error()
						trial.Attempts = append(trial.Attempts, a)
						cancel()
						break
					}
					if len(response.ToolCalls) != 1 {
						a.Error = "expected exactly one tool call"
						trial.Attempts = append(trial.Attempts, a)
						cancel()
						break
					}
					call := response.ToolCalls[0]
					selected := byName[call.Name]
					var result tool.Result
					if selected == nil {
						err = fmt.Errorf("unknown tool %s", call.Name)
					} else {
						err = tool.ValidateArguments(selected, call.Arguments)
						if err == nil {
							result, err = selected.Call(ctx, tool.Call{Arguments: call.Arguments})
						}
					}
					cancel()
					a.Result = result.Content.Text()
					if err != nil {
						a.Error = err.Error()
					}
					a.Correct, a.WrongExisting = scorePathCall(dir, tc, call, a.Result, err)
					trial.Attempts = append(trial.Attempts, a)
					if a.Correct || a.WrongExisting {
						break
					} // Silent wrong-file successes have no natural correction signal.
					messages = append(messages, provider.Message{Role: "assistant", Content: content.Text(response.Content), ToolCalls: response.ToolCalls})
					receipt := a.Result
					if a.Error != "" {
						receipt = "Tool error: " + a.Error
					}
					messages = append(messages, provider.Message{Role: "tool", ToolCallID: call.ID, Content: content.Text(receipt)})
				}
				if err := json.NewEncoder(log).Encode(trial); err != nil {
					t.Fatal(err)
				}
				if err := log.Sync(); err != nil {
					t.Fatal(err)
				}
				for path, text := range fixtures {
					b, err := os.ReadFile(filepath.Join(dir, path))
					if err != nil || string(b) != text {
						t.Fatalf("fixture changed: %s", path)
					}
				}
				last := trial.Attempts[len(trial.Attempts)-1]
				t.Logf("%s repeat=%d variant=%s first=%v eventual=%v attempts=%d wrong_existing=%v", tc.name, repeat+1, variant, trial.Attempts[0].Correct, last.Correct, len(trial.Attempts), last.WrongExisting)
			}
		}
	}
}

func scorePathCall(dir string, tc pathCase, call provider.ToolCall, result string, callErr error) (bool, bool) {
	if callErr != nil {
		return false, false
	}
	var args struct {
		Input struct{ Path, Command, Relation, Content string }
	}
	if json.Unmarshal(call.Arguments, &args) != nil {
		return false, false
	}
	if call.Name == "shell" {
		var out tool.ShellResult
		if json.Unmarshal([]byte(result), &out) != nil || out.ExitCode == nil || *out.ExitCode != 0 || out.TimedOut || out.Cancelled {
			return false, false
		}
		if tc.name == "directory_discovery" {
			return strings.Contains(out.Output, "README.md") && strings.Contains(out.Output, "prefix.go"), false
		}
		if tc.name == "new_parent" {
			info, err := os.Stat(filepath.Join(dir, "scratch"))
			return err == nil && info.IsDir(), false
		}
		if tc.wantPath == "prefix.go" {
			if strings.Contains(out.Output, "DECOY_TARGET") {
				return false, true
			}
			return strings.Contains(out.Output, "ROOT_TARGET"), false
		}
		if tc.name == "spaced_filename" {
			if strings.Contains(out.Output, "return legacy.RateFor(") {
				return false, true
			}
			return strings.Contains(out.Output, "return pricing.RateFor("), false
		}
		if tc.name == "workspace_root" {
			return strings.Contains(out.Output, "Path fixture contract. Implement SharedPrefix in prefix.go, package config."), false
		}
		return false, false
	}
	path := args.Input.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	expected := filepath.Join(dir, tc.wantPath)
	path = filepath.Clean(path)
	if canonical, err := filepath.EvalSymlinks(path); err == nil {
		path = canonical
	}
	// A preparatory read of README.md is not a wrong-target success. Only the
	// deliberately misleading alternate source files establish that failure.
	decoy := ""
	if tc.wantPath == "prefix.go" {
		decoy = filepath.Join(dir, "config/prefix.go")
	}
	if tc.wantPath == "preview/emoji file.go" {
		decoy = filepath.Join(dir, "preview/emoji/file.go")
	}
	wrong := decoy != "" && path == decoy && (call.Name == "read_file" || call.Name == "lsp_inspect" || call.Name == "lsp_outline" || call.Name == "lsp_navigate")
	if path != expected {
		return false, wrong
	}
	if tc.wantTool == "read_file" {
		// Inspect and outline may supply the same source evidence as read_file.
		if call.Name == "lsp_inspect" {
			var out lsp.Inspection
			return json.Unmarshal([]byte(result), &out) == nil && out.Location.Excerpt != "", false
		}
		if call.Name == "lsp_outline" {
			var out lsp.Page
			if json.Unmarshal([]byte(result), &out) == nil {
				for _, item := range out.Items {
					if strings.Contains(item.Excerpt, "ROOT_TARGET") {
						return true, false
					}
				}
			}
			return false, false
		}
	}
	if call.Name != tc.wantTool {
		return false, false
	}
	switch call.Name {
	case "read_file":
		var out tool.ReadFileResult
		return json.Unmarshal([]byte(result), &out) == nil && out.Content != "", false
	case "write_file":
		b, err := os.ReadFile(expected)
		return err == nil && strings.TrimSuffix(string(b), "\n") == "path probe", false
	case "lsp_navigate":
		var out lsp.Page
		if json.Unmarshal([]byte(result), &out) != nil {
			return false, false
		}
		return args.Input.Relation == "definition" && len(out.Items) == 1 && out.Items[0].Path == "pricing/rate.go" && out.Metadata.Freshness != "stale" && !out.Metadata.Partial, false
	}
	return false, false
}
