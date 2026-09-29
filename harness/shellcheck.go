package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/lsp"
	"github.com/stevemurr/strap/tool"
)

// A shell check appends, to a shell result, the language server's errors for
// the packages of the files the command changed, as a write check does for
// write_file. Models write throwaway programs through heredocs, and in the
// easy+medium ladder with write checks (2026-09-26) those unchecked writes
// were the main remaining source of builds that failed to compile.

// shellCheckNote joins the shell tool's description when commands are checked.
const shellCheckNote = " When the command changes files a language server covers, the result's diagnostics field lists the errors it reports for their packages and, for Go, the workspace packages they import."

type shellCheck struct {
	tool.Tool
	languages *lsp.Manager
	scratch   *scratchLanguages
	dir       string // The shell's working directory.
}

// checkShell wraps the shell among tools with a shell check.
func checkShell(tools []tool.Tool, languages *lsp.Manager, scratch *scratchLanguages, dir string) {
	for i, t := range tools {
		if t.Definition().Name == "shell" {
			tools[i] = shellCheck{Tool: t, languages: languages, scratch: scratch, dir: dir}
		}
	}
}

func (t shellCheck) InputContract() tool.Contract {
	return t.Tool.(interface{ InputContract() tool.Contract }).InputContract()
}
func (t shellCheck) Validate() error { return tool.ValidateTool(t.Tool) }
func (t shellCheck) BookkeepingParameters() []string {
	if b, ok := t.Tool.(interface{ BookkeepingParameters() []string }); ok {
		return b.BookkeepingParameters()
	}
	return nil
}

func (t shellCheck) Call(ctx context.Context, c tool.Call) (tool.Result, error) {
	var in struct {
		Input struct {
			Command string `json:"command"`
		} `json:"input"`
	}
	if json.Unmarshal(c.Arguments, &in) != nil || in.Input.Command == "" {
		return t.Tool.Call(ctx, c)
	}
	root := t.languages.Config().Dir
	before, _ := snapshot(root)
	started := time.Now()
	result, err := t.Tool.Call(ctx, c)
	if err != nil {
		return result, err
	}
	after, _ := snapshot(root)
	var changed []string
	for _, rel := range changedFiles(before, after) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if _, exists := after[rel]; exists && t.languages.Handles(path) {
			changed = append(changed, path)
		}
	}
	// Files outside the workspace are found from the command itself: an
	// auditor's scratch module in /tmp, written through a heredoc.
	for _, path := range writeTargets(in.Input.Command, t.dir) {
		if within(root, path) || !t.languages.Handles(path) || slices.Contains(changed, path) {
			continue
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() && !info.ModTime().Before(started.Add(-time.Second)) {
			changed = append(changed, path)
		}
	}
	if len(changed) == 0 {
		return result, nil
	}
	slices.Sort(changed)
	// One check per package: each reports its package's files together.
	var reports []string
	checked := map[string]bool{}
	for _, path := range changed {
		if checked[filepath.Dir(path)] {
			continue
		}
		checked[filepath.Dir(path)] = true
		if r := checkPackage(ctx, t.languages, t.scratch, path); r != "" {
			reports = append(reports, r)
		}
	}
	if len(reports) == 0 {
		return result, nil
	}
	names := make([]string, len(changed))
	for i, path := range changed {
		names[i] = relative(root, path)
	}
	text := "This command changed " + strings.Join(names, ", ") + ".\n" + strings.Join(reports, "\n")
	result.Content = withDiagnostics(result.Content, text)
	return result, nil
}

// withDiagnostics adds text to a shell result as its diagnostics field, so the
// result stays one JSON object: the execution receipt embeds it as an object
// and the recorded runs are read back from it. Output that is not an object
// gets the text after it instead.
func withDiagnostics(c content.Content, text string) content.Content {
	raw := strings.TrimSpace(c.Text())
	field, err := json.Marshal(text)
	if err == nil && strings.HasPrefix(raw, "{") && strings.HasSuffix(raw, "}") && json.Valid([]byte(raw)) {
		joined := strings.TrimSuffix(raw, "}")
		if strings.TrimSpace(joined) != "{" {
			joined += ","
		}
		if spliced := joined + `"diagnostics":` + string(field) + "}"; json.Valid([]byte(spliced)) {
			return content.Text(spliced)
		}
	}
	return append(c, content.Text(text)...)
}

var (
	heredocStart  = regexp.MustCompile(`<<-?\s*['"]?(\w+)['"]?`)
	cdCommand     = regexp.MustCompile(`^\s*cd\s+([^\s;&|]+)\s*$`)
	redirectPath  = regexp.MustCompile(`(?:^|[^0-9&>])>>?\s*([^\s;&|<>()'"]+)`)
	teePath       = regexp.MustCompile(`\btee\s+(?:-a\s+)?([^\s;&|<>()'"]+)`)
	copyMovePaths = regexp.MustCompile(`^\s*(?:cp|mv)\s+(?:-\S+\s+)*(\S+)\s+(\S+)\s*$`)
	segmentSplit  = regexp.MustCompile(`&&|\|\||;`)
)

// writeTargets lists the files command writes by redirection, tee, cp or mv,
// resolved against the directory each part of the command runs in, starting
// from dir and following cd. Heredoc bodies are skipped: they are file text,
// where > means anything.
func writeTargets(command, dir string) []string {
	var out []string
	cwd := dir
	resolve := func(p string) string {
		p = tool.ExpandHome(p)
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		p = filepath.Clean(p)
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		} else if real, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
			p = filepath.Join(real, filepath.Base(p))
		}
		return p
	}
	delimiter := ""
	for _, line := range strings.Split(command, "\n") {
		if delimiter != "" {
			if strings.TrimSpace(line) == delimiter {
				delimiter = ""
			}
			continue
		}
		if m := heredocStart.FindStringSubmatch(line); m != nil {
			delimiter = m[1]
		}
		for _, segment := range segmentSplit.Split(line, -1) {
			if m := cdCommand.FindStringSubmatch(segment); m != nil {
				cwd = resolve(m[1])
				continue
			}
			if m := copyMovePaths.FindStringSubmatch(segment); m != nil {
				target := resolve(m[2])
				if info, err := os.Stat(target); err == nil && info.IsDir() {
					target = filepath.Join(target, filepath.Base(m[1]))
				}
				out = append(out, target)
				continue
			}
			for _, m := range redirectPath.FindAllStringSubmatch(segment, -1) {
				if m[1] != "/dev/null" {
					out = append(out, resolve(m[1]))
				}
			}
			for _, m := range teePath.FindAllStringSubmatch(segment, -1) {
				out = append(out, resolve(m[1]))
			}
		}
	}
	return out
}

// within reports whether path is root or inside it.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
