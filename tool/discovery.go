package tool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// Discovery tools find files and text without the shell. Names, parameters and
// output follow Qwen Code's glob, grep_search and list_directory, the harness
// the Qwen models are trained against. One deliberate difference: grep_search
// shows matching lines untrimmed, so a line copied into edit_file keeps its
// real indentation. Results are plain text, like read_file under EditMerge.

const (
	discoveryEntries   = 100    // files or directory entries shown
	discoveryScanCap   = 10_000 // files examined before a search stops
	grepOutputChars    = 20_000
	grepLineChars      = 300
	grepDefaultMatches = 200
)

// Directories that are never searched: version control and dependency trees.
var skippedDiscoveryDirs = map[string]bool{".git": true, "node_modules": true, ".hg": true, ".svn": true}

type globArgs struct {
	Pattern string  `json:"pattern"`
	Path    *string `json:"path"`
}

type grepArgs struct {
	Pattern string  `json:"pattern"`
	Glob    *string `json:"glob"`
	Path    *string `json:"path"`
	Limit   *int    `json:"limit"`
}

type listArgs struct {
	Path *string `json:"path"`
}

func (f *Files) discoveryTools() []Tool {
	return []Tool{
		builtin("glob",
			fmt.Sprintf("Find files by name pattern. Supports glob patterns like \"**/*.go\" or \"cmd/**/*_test.go\"; a pattern without a slash, like \"*.go\" or \"stock.go\", matches file names at any depth. Returns absolute paths, newest first, at most %d. Searches path (default %s); skips .git and node_modules. Use it instead of guessing where a file is.", discoveryEntries, f.config.Dir),
			f.glob, MinLength("pattern", 1), Nullable("path", "search the working directory"), MinLength("path", 1)),
		builtin("grep_search",
			fmt.Sprintf("Search file contents with a regular expression (RE2 syntax, e.g. \"func\\s+New\", \"log.*Error\"), case-insensitive unless the pattern sets (?-i). Returns matching lines grouped by file as \"L<line>: <text>\", with each line exactly as stored; the L<line>: prefix is not file content. Searches path, a file or directory (default %s), optionally only files whose names match glob (e.g. \"*.go\", \"*.{ts,tsx}\"); skips .git, node_modules and binary files. limit caps the matching lines shown.", f.config.Dir),
			f.grep, MinLength("pattern", 1), Nullable("glob", "search every file"), MinLength("glob", 1), Nullable("path", "search the working directory"), MinLength("path", 1), Nullable("limit", "show up to 200 matching lines"), Minimum("limit", 1), Maximum("limit", 2000)),
		builtin("list_directory",
			fmt.Sprintf("List the files and subdirectories directly inside a directory, subdirectories first, marked [DIR]. Accepts absolute paths; relative paths resolve from %s (the default).", f.config.Dir),
			f.listDirectory, Nullable("path", "list the working directory"), MinLength("path", 1)),
	}
}

// searchRoot resolves an optional path argument to an existing file or directory.
func (f *Files) searchRoot(p *string) (string, fs.FileInfo, error) {
	root := f.config.Dir
	if p != nil {
		root = *p
		if !filepath.IsAbs(root) {
			root = filepath.Join(f.config.Dir, root)
		}
	}
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil, fmt.Errorf("no such file or directory: %s; list_directory or glob can show what exists", root)
		}
		return "", nil, err
	}
	return root, info, nil
}

// compileGlob turns a glob into a matcher over slash-separated paths relative
// to the search root. A pattern without a slash matches base names at any
// depth; ** matches any number of directories; {a,b} alternates.
func compileGlob(pattern string) (func(rel string) bool, error) {
	pattern = strings.TrimPrefix(filepath.ToSlash(pattern), "./")
	var alts []string
	if err := expandBraces(pattern, &alts); err != nil {
		return nil, err
	}
	for _, p := range alts {
		if _, err := path.Match(strings.ReplaceAll(p, "**", "*"), ""); err != nil {
			return nil, fmt.Errorf("invalid glob %q: %v", pattern, err)
		}
	}
	return func(rel string) bool {
		for _, p := range alts {
			if !strings.Contains(p, "/") {
				if ok, _ := path.Match(p, path.Base(rel)); ok {
					return true
				}
				continue
			}
			if matchSegments(strings.Split(p, "/"), strings.Split(rel, "/")) {
				return true
			}
		}
		return false
	}, nil
}

func expandBraces(p string, out *[]string) error {
	open := strings.IndexByte(p, '{')
	if open < 0 {
		*out = append(*out, p)
		return nil
	}
	end := strings.IndexByte(p[open:], '}')
	if end < 0 {
		return fmt.Errorf("invalid glob %q: unclosed {", p)
	}
	end += open
	for _, alt := range strings.Split(p[open+1:end], ",") {
		if err := expandBraces(p[:open]+alt+p[end+1:], out); err != nil {
			return err
		}
	}
	return nil
}

func matchSegments(pat, rel []string) bool {
	if len(pat) == 0 {
		return len(rel) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(rel); i++ {
			if matchSegments(pat[1:], rel[i:]) {
				return true
			}
		}
		return false
	}
	if len(rel) == 0 {
		return false
	}
	if ok, _ := path.Match(pat[0], rel[0]); !ok {
		return false
	}
	return matchSegments(pat[1:], rel[1:])
}

type foundFile struct {
	path string
	mod  int64
}

// walkFiles visits regular files under root in lexical order, skipping
// version-control and dependency directories, until visit returns false or the
// scan cap is reached. It reports whether the cap cut the walk short.
func walkFiles(ctx context.Context, root string, visit func(abs, rel string, d fs.DirEntry) bool) (capped bool, err error) {
	seen := 0
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil // unreadable entries are skipped, not fatal
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && skippedDiscoveryDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if seen++; seen > discoveryScanCap {
			capped = true
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(root, p)
		if !visit(p, filepath.ToSlash(rel), d) {
			return filepath.SkipAll
		}
		return nil
	})
	return capped, err
}

func (f *Files) glob(ctx context.Context, _ Call, args globArgs) (Result, error) {
	match, err := compileGlob(args.Pattern)
	if err != nil {
		return Result{}, err
	}
	root, info, err := f.searchRoot(args.Path)
	if err != nil {
		return Result{}, err
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("%s is a file, not a directory to search", root)
	}
	var found []foundFile
	capped, err := walkFiles(ctx, root, func(abs, rel string, d fs.DirEntry) bool {
		if match(rel) {
			var mod int64
			if fi, err := d.Info(); err == nil {
				mod = fi.ModTime().UnixNano()
			}
			found = append(found, foundFile{abs, mod})
		}
		return true
	})
	if err != nil {
		return Result{}, err
	}
	if len(found) == 0 {
		return Text(fmt.Sprintf("No files found matching %q in %s.\n", args.Pattern, root) + cappedNote(capped)), nil
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].mod != found[j].mod {
			return found[i].mod > found[j].mod
		}
		return found[i].path < found[j].path
	})
	var b strings.Builder
	fmt.Fprintf(&b, "Found %d file(s) matching %q in %s, newest first:\n---\n", len(found), args.Pattern, root)
	for _, file := range found[:min(len(found), discoveryEntries)] {
		b.WriteString(file.path + "\n")
	}
	if len(found) > discoveryEntries {
		fmt.Fprintf(&b, "[%d more not shown; narrow the pattern or path]\n", len(found)-discoveryEntries)
	}
	b.WriteString(cappedNote(capped))
	return Text(b.String()), nil
}

func cappedNote(capped bool) string {
	if !capped {
		return ""
	}
	return fmt.Sprintf("[stopped after examining %d files; narrow the path]\n", discoveryScanCap)
}

func (f *Files) grep(ctx context.Context, _ Call, args grepArgs) (Result, error) {
	// Case-insensitive by default, as in Qwen Code; (?-i) in the pattern overrides it.
	re, err := regexp.Compile("(?i)" + args.Pattern)
	if err != nil {
		return Result{}, fmt.Errorf("invalid regular expression %q: %v", args.Pattern, err)
	}
	include := func(string) bool { return true }
	if args.Glob != nil {
		if include, err = compileGlob(*args.Glob); err != nil {
			return Result{}, err
		}
	}
	root, info, err := f.searchRoot(args.Path)
	if err != nil {
		return Result{}, err
	}
	limit := grepDefaultMatches
	if args.Limit != nil {
		limit = *args.Limit
	}
	type fileMatches struct {
		path  string
		lines []string
	}
	var results []fileMatches
	total, shown := 0, 0
	scan := func(abs string) {
		data, err := os.ReadFile(abs)
		if err != nil || len(data) > f.config.MaxFileBytes || bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 || !utf8.Valid(data) {
			return
		}
		var lines []string
		for n, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSuffix(line, "\r")
			if !re.MatchString(line) {
				continue
			}
			total++
			if shown < limit {
				shown++
				if len(line) > grepLineChars {
					cut := line[:grepLineChars]
					for !utf8.ValidString(cut) {
						cut = cut[:len(cut)-1]
					}
					line = cut + "…"
				}
				lines = append(lines, fmt.Sprintf("L%d: %s", n+1, line))
			}
		}
		if len(lines) > 0 {
			results = append(results, fileMatches{abs, lines})
		}
	}
	capped := false
	if info.IsDir() {
		capped, err = walkFiles(ctx, root, func(abs, rel string, _ fs.DirEntry) bool {
			if include(rel) {
				scan(abs)
			}
			return true
		})
		if err != nil {
			return Result{}, err
		}
	} else {
		scan(root)
	}
	filter := ""
	if args.Glob != nil {
		filter = fmt.Sprintf(" (files matching %q)", *args.Glob)
	}
	if total == 0 {
		return Text(fmt.Sprintf("No matches found for pattern %q in %s%s.\n", args.Pattern, root, filter) + cappedNote(capped)), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Found %d matching line(s) for pattern %q in %s%s:\n---\n", total, args.Pattern, root, filter)
	cut := false
	for _, r := range results {
		chunk := "File: " + r.path + "\n" + strings.Join(r.lines, "\n") + "\n"
		if b.Len()+len(chunk) > grepOutputChars {
			cut = true
			break
		}
		b.WriteString(chunk)
	}
	switch {
	case cut:
		b.WriteString("[more matches not shown: output limit reached; narrow the pattern, glob or path]\n")
	case total > shown:
		fmt.Fprintf(&b, "[%d more matching line(s) not shown; raise limit or narrow the search]\n", total-shown)
	}
	b.WriteString(cappedNote(capped))
	return Text(b.String()), nil
}

func (f *Files) listDirectory(ctx context.Context, _ Call, args listArgs) (Result, error) {
	root, info, err := f.searchRoot(args.Path)
	if err != nil {
		return Result{}, err
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("%s is a file, not a directory; read it with read_file", root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return Result{}, err
	}
	slices.SortStableFunc(entries, func(a, b fs.DirEntry) int {
		if a.IsDir() != b.IsDir() {
			if a.IsDir() {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name(), b.Name())
	})
	var b strings.Builder
	fmt.Fprintf(&b, "Listed %d item(s) in %s:\n---\n", len(entries), root)
	for _, e := range entries[:min(len(entries), discoveryEntries)] {
		if e.IsDir() {
			b.WriteString("[DIR] ")
		}
		b.WriteString(e.Name() + "\n")
	}
	if len(entries) > discoveryEntries {
		fmt.Fprintf(&b, "[%d more not shown; use glob to narrow]\n", len(entries)-discoveryEntries)
	}
	return Text(b.String()), nil
}
