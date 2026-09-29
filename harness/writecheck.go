package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/lsp"
	"github.com/stevemurr/strap/tool"
	"golang.org/x/mod/modfile"
)

// A write check appends, to a successful write_file or edit_file result, the
// errors the language server reports for the changed file's package once it
// has analysed the change. Compile errors otherwise surfaced only when the
// model spent a turn running the build: across the ladder's easy runs
// (2026-09-26), 17 of the auditors' 123 go runs and 4 of the implementors' 67
// failed to compile.

// writeCheckWait bounds how long a write waits for the servers' analysis.
const writeCheckWait = 5 * time.Second

// writeCheckErrors bounds the errors listed in one result.
const writeCheckErrors = 10

// writeCheckNote joins the write tools' descriptions when writes are checked.
const writeCheckNote = " When a language server covers the file, the result also lists the errors it reports after the change for the file's package and, for Go, the workspace packages it imports."

type writeCheck struct {
	tool.Tool
	languages *lsp.Manager      // The workspace's, or a copy's.
	scratch   *scratchLanguages // For files written outside languages' directory.
	dir       string            // Relative paths resolve from here, as the file tools resolve them.
}

// checkWrites wraps the write tools among tools with a write check.
func checkWrites(tools []tool.Tool, languages *lsp.Manager, scratch *scratchLanguages, dir string) {
	for i, t := range tools {
		if n := t.Definition().Name; n == "write_file" || n == "edit_file" {
			tools[i] = writeCheck{Tool: t, languages: languages, scratch: scratch, dir: dir}
		}
	}
}

func (t writeCheck) InputContract() tool.Contract {
	return t.Tool.(interface{ InputContract() tool.Contract }).InputContract()
}
func (t writeCheck) Validate() error { return tool.ValidateTool(t.Tool) }
func (t writeCheck) BookkeepingParameters() []string {
	if b, ok := t.Tool.(interface{ BookkeepingParameters() []string }); ok {
		return b.BookkeepingParameters()
	}
	return nil
}

func (t writeCheck) Call(ctx context.Context, c tool.Call) (tool.Result, error) {
	result, err := t.Tool.Call(ctx, c)
	if err != nil {
		return result, err
	}
	var in struct {
		Input struct {
			Path string `json:"path"`
		} `json:"input"`
	}
	if json.Unmarshal(c.Arguments, &in) != nil || in.Input.Path == "" {
		return result, nil
	}
	path := tool.ExpandHome(in.Input.Path)
	if !filepath.IsAbs(path) {
		path = filepath.Join(t.dir, path)
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if !t.languages.Handles(path) {
		return result, nil
	}
	if report := checkPackage(ctx, t.languages, t.scratch, path); report != "" {
		result.Content = append(result.Content, content.Text(report)...)
	}
	return result, nil
}

// checkPackage checks path's package, and for a Go file the workspace
// packages it imports, each file with the manager that covers it: primary, or
// for a file outside primary's directory, scratch's manager for its project.
// An imported package with errors breaks the build of the file that imports
// it, where the changed package alone checked clean (ladder medium-02,
// 2026-09-26: a stray package main file in the imported workspace root). It
// returns the report, or "" when no server gave an answer for path.
func checkPackage(ctx context.Context, primary *lsp.Manager, scratch *scratchLanguages, path string) string {
	managerFor := func(file string) *lsp.Manager {
		if primary.Covers(file) {
			return primary
		}
		if scratch == nil {
			return nil
		}
		if m := scratch.forPath(file); m != nil && m.Covers(file) {
			return m
		}
		return nil
	}
	own := managerFor(path)
	if own == nil {
		return ""
	}
	var order []*lsp.Manager
	groups := map[*lsp.Manager][]string{}
	add := func(file string) {
		m := managerFor(file)
		if m == nil || len(groups[m]) >= 32 || slices.Contains(groups[m], file) {
			return
		}
		if groups[m] == nil {
			order = append(order, m)
		}
		groups[m] = append(groups[m], file)
	}
	add(path)
	for _, f := range packageFiles(filepath.Dir(path), filepath.Ext(path)) {
		add(f)
	}
	imports := goImportDirs(path)
	for _, dir := range imports {
		for _, f := range packageFiles(dir, filepath.Ext(path)) {
			add(f)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 2*writeCheckWait)
	defer cancel()
	deadline := time.Now().Add(writeCheckWait)
	// A file outside the primary manager's directory is named in full: its
	// manager's directory means nothing to the model.
	show := func(m *lsp.Manager, p string) string {
		if m == primary {
			return p
		}
		return filepath.Join(m.Config().Dir, filepath.FromSlash(p))
	}
	name := show(own, relative(own.Config().Dir, path))
	var own_, others []string
	for _, m := range order {
		page, ok := diagnose(ctx, m, groups[m], deadline)
		if !ok {
			if m == own {
				return ""
			}
			continue
		}
		if m == own {
			checked := false
			for _, check := range page.Metadata.Checks {
				if show(m, check.Path) == name {
					checked = true
					if check.Freshness != "synchronized" {
						return fmt.Sprintf("Language server results for %s were not ready within %s; run your build to check it.", name, writeCheckWait)
					}
				}
			}
			if !checked {
				return "" // No server answered for the file, such as one that failed to start.
			}
		}
		for _, item := range page.Items {
			if item.Severity != "error" {
				continue
			}
			line := show(m, item.Path)
			if item.Selection != nil {
				line += fmt.Sprintf(":%d:%d", item.Selection.Start.Line, item.Selection.Start.Column)
			}
			line += " " + strings.TrimSpace(item.Message)
			if show(m, item.Path) == name {
				own_ = append(own_, line)
			} else {
				others = append(others, line)
			}
		}
	}
	errors := append(own_, others...)
	if len(errors) == 0 {
		if len(imports) > 0 {
			return fmt.Sprintf("No errors reported for %s, the other files of its package or the workspace packages it imports.", name)
		}
		return fmt.Sprintf("No errors reported for %s or the other files of its package.", name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The language server reports %d error(s) after this change:", len(errors))
	for i, e := range errors {
		if i == writeCheckErrors {
			fmt.Fprintf(&b, "\n… and %d more; lsp_diagnostics lists them all.", len(errors)-writeCheckErrors)
			break
		}
		b.WriteString("\n" + e)
	}
	return b.String()
}

// diagnose asks m for the diagnostics of paths once it has analysed them, by
// deadline; ok is false when it gave no answer.
func diagnose(ctx context.Context, m *lsp.Manager, paths []string, deadline time.Time) (page lsp.Page, ok bool) {
	for {
		p, err := m.Diagnostics(ctx, lsp.DiagnosticQuery{Paths: paths, PageQuery: lsp.PageQuery{Limit: 200}, Wait: time.Until(deadline), Resync: true})
		if err != nil {
			return lsp.Page{}, false
		}
		page = p
		// A change the manager learns of after it synchronized the files, such
		// as the file watcher's event for this write, leaves the result stale.
		if page.Metadata.Freshness != "stale" || time.Until(deadline) < 500*time.Millisecond {
			return page, true
		}
	}
}

// packageFiles lists dir's files with extension ext.
func packageFiles(dir, ext string) []string {
	var out []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ext {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// relative is path as a manager rooted at root names it.
func relative(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return path
}

// goImportDirs lists the directories of the packages a Go file imports from
// its own module or a module its go.mod replaces with a local directory,
// other than the file's own directory. The file's module is the nearest
// go.mod above it.
func goImportDirs(path string) []string {
	if filepath.Ext(path) != ".go" {
		return nil
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		return nil
	}
	root := filepath.Dir(path)
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			return nil
		}
		root = parent
	}
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil
	}
	// ParseLax would drop replace directives; it only reads the module path
	// of a go.mod the strict parser rejects.
	mod, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		mod, err = modfile.ParseLax("go.mod", data, nil)
	}
	if err != nil || mod.Module == nil {
		return nil
	}
	// Module path prefixes and the directories they name.
	local := map[string]string{mod.Module.Mod.Path: root}
	for _, r := range mod.Replace {
		if modfile.IsDirectoryPath(r.New.Path) {
			dir := r.New.Path
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(root, dir)
			}
			local[r.Old.Path] = filepath.Clean(dir)
		}
	}
	var dirs []string
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		for prefix, dir := range local {
			rest, ok := strings.CutPrefix(importPath, prefix)
			if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
				continue
			}
			d := filepath.Join(dir, filepath.FromSlash(rest))
			if real, err := filepath.EvalSymlinks(d); err == nil {
				d = real
			}
			if info, err := os.Stat(d); err == nil && info.IsDir() && d != filepath.Dir(path) && !slices.Contains(dirs, d) {
				dirs = append(dirs, d)
			}
		}
	}
	return dirs
}
