package tool

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Under EditAnchors, read_file labels every line and edit_file names lines by
// those labels instead of by retyped text. Whitespace, escaping and duplicate
// text therefore cannot misdirect an edit, and a stale view cannot apply: a
// label names one line for as long as that line is unchanged, and is never
// reused once the line changes or disappears.

type anchoredEditArgs struct {
	Path  string  `json:"path"`
	Op    string  `json:"op"`
	Start string  `json:"start"`
	End   *string `json:"end"`
	New   string  `json:"new"`
}

type EditLinesResult struct {
	Path       string `json:"path"`
	Removed    string `json:"removed"` // replaced lines without labels, bounded
	View       string `json:"view"`    // edited region with fresh labels and two lines of context
	TotalLines int    `json:"total_lines"`
}

func (f *Files) anchoredTools() []Tool {
	return []Tool{
		builtin("read_file",
			fmt.Sprintf("Read a UTF-8 text file. Accepts absolute paths; relative paths resolve from %s. Returns lines as NUMBER:LABEL│text, starting at offset (1-based, default 1), up to limit (default 200, maximum 2000). The NUMBER:LABEL│ prefix is not file content; pass NUMBER:LABEL to edit_file to name that line. Files are limited to %d bytes and output to %d bytes. Symlinks resolve to their targets.", f.config.Dir, f.config.MaxFileBytes, f.config.OutputLimit),
			f.read, Nullable("offset", "start at the beginning"), Nullable("limit", "use the default read limit"), MinLength("path", 1), Minimum("offset", 1), Minimum("limit", 1), Maximum("limit", 2000)),
		builtin("write_file",
			fmt.Sprintf("Create a UTF-8 text file. Accepts absolute paths; relative paths resolve from %s. The file must not exist yet or must be empty; change existing files with edit_file. Content is limited to %d bytes. Content is literal text, including newlines; do not add Markdown fences or shell heredocs. Missing parent directories are created.", f.config.Dir, f.config.MaxFileBytes),
			f.create, MinLength("path", 1)),
		builtin("edit_file",
			fmt.Sprintf("Change lines of an existing UTF-8 text file, naming them by the NUMBER:LABEL that read_file shows; never retype existing text to locate it. op replace swaps lines start through end inclusive for new, and an empty new deletes them. op insert_before or insert_after adds new next to start; end must be null. A label whose line changed after it was shown is rejected with the current nearby lines and their labels. The result shows the removed text and the edited region with fresh labels. Accepts absolute paths; relative paths resolve from %s. The resulting file is limited to %d bytes.", f.config.Dir, f.config.MaxFileBytes),
			f.editLines, MinLength("path", 1), Enum("op", "replace", "insert_before", "insert_after"), MinLength("start", 1), MinLength("end", 1),
			Description("start", "Line label exactly as read_file shows it, e.g. 12:kqx"),
			Nullable("end", "replace only the start line; required for inserts"), Description("end", "Last line label to replace, inclusive"),
			Description("new", "Literal replacement or inserted text without labels; one trailing newline is optional")),
	}
}

func (f *Files) create(ctx context.Context, _ Call, args writeArgs) (Result, error) {
	if err := f.lock(ctx); err != nil {
		return Result{}, err
	}
	changed := ""
	defer func() {
		f.unlock()
		if changed != "" && f.config.OnChange != nil {
			f.config.OnChange(changed)
		}
	}()
	path, err := f.resolveForWrite(args.Path, args.Content)
	if err != nil {
		return Result{}, err
	}
	existing, err := f.readText(ctx, path)
	switch {
	case err == nil && existing != "":
		return Result{}, errors.New("file already exists; change it with edit_file using the line labels read_file shows (to replace everything, replace from its first label to its last)")
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return Result{}, err
	}
	if err := f.atomicWriteText(ctx, path, args.Content); err != nil {
		return Result{}, err
	}
	changed = path
	f.labelsFor(path, existing).replaceAll(args.Content)
	return JSON(WriteFileResult{Path: args.Path, BytesWritten: len(args.Content)})
}

func (f *Files) editLines(ctx context.Context, _ Call, args anchoredEditArgs) (result Result, err error) {
	if err := f.lock(ctx); err != nil {
		return Result{}, err
	}
	changed := ""
	defer func() {
		f.unlock()
		if changed != "" && f.config.OnChange != nil {
			f.config.OnChange(changed)
		}
	}()
	path, err := f.resolve(args.Path)
	if err != nil {
		return Result{}, err
	}
	text, err := f.readText(ctx, path)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		if err != nil {
			end := ""
			if args.End != nil {
				end = *args.End
			}
			err = &DiagnosticError{Cause: err, Detail: Diagnostic{Kind: "file_edit", Edit: &EditDiagnostic{RequestedPath: args.Path, ResolvedPath: path, Before: text, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(text))), New: args.New, Op: args.Op, Start: args.Start, End: end}}}
		}
	}()
	labels := f.labelsFor(path, text)
	lines, trailing := splitLines(text)
	if len(lines) == 0 {
		return Result{}, errors.New("file is empty and has no line labels; give it content with write_file")
	}
	first, err := labels.locate("start", args.Start, lines)
	if err != nil {
		return Result{}, err
	}
	var at, cut int // new lines go at index at, replacing cut lines
	switch args.Op {
	case "replace":
		last := first
		if args.End != nil {
			if last, err = labels.locate("end", *args.End, lines); err != nil {
				return Result{}, err
			}
			if last < first {
				return Result{}, fmt.Errorf("end is line %d, before start at line %d; end must be at or after start", last+1, first+1)
			}
		}
		at, cut = first, last-first+1
	case "insert_before", "insert_after":
		if args.End != nil {
			return Result{}, fmt.Errorf("end must be null for %s; it inserts next to the start line", args.Op)
		}
		if args.New == "" {
			return Result{}, errors.New("new is empty; there is nothing to insert")
		}
		at = first
		if args.Op == "insert_after" {
			at++
		}
	default:
		return Result{}, fmt.Errorf("unknown op %q", args.Op)
	}
	added := newLines(args.New, lines)
	updated := slices.Concat(lines[:at], added, lines[at+cut:])
	content := joinLines(updated, trailing)
	if err := f.atomicWriteText(ctx, path, content); err != nil {
		return Result{}, err
	}
	changed = path
	labels.splice(content, at, cut, len(added))
	from, to := max(0, at-2), min(len(updated), at+len(added)+2)
	return JSON(EditLinesResult{
		Path:       args.Path,
		Removed:    bounded(lines[at:at+cut], 30, func(i int) string { return "" }),
		View:       bounded(updated[from:to], 60, func(i int) string { return labelPrefix(from+i, labels.tags[from+i]) }),
		TotalLines: len(updated),
	})
}

// lineLabels is the identity Files has given each line of one file. A label
// survives edits to other lines, including edits made outside Files (shell,
// formatters, other processes), which are reconciled by diffing against the
// text the labels last described.
type lineLabels struct {
	text    string
	tags    []string
	index   map[string]int // live label -> line index
	retired map[string]int // dead label -> current index nearest where its line was
	issued  int
}

// labelsFor returns the labels for text, reconciling them with any change made
// since Files last saw the file. The caller holds the lock.
func (f *Files) labelsFor(path, text string) *lineLabels {
	l := f.labels[path]
	if l == nil {
		l = &lineLabels{retired: map[string]int{}}
		f.labels[path] = l
		lines, _ := splitLines(text)
		l.set(text, l.mintN(len(lines)))
		return l
	}
	if l.text != text {
		l.reconcile(text)
	}
	return l
}

func (l *lineLabels) set(text string, tags []string) {
	l.text, l.tags = text, tags
	l.index = make(map[string]int, len(tags))
	for i, tag := range tags {
		l.index[tag] = i
	}
}

// Labels are three or more lowercase letters, issued in a scrambled order so
// neighbours do not look alike. Each width is a bijection over its space and
// widths grow, so a file never sees the same label twice.
func (l *lineLabels) mint() string {
	n := l.issued
	l.issued++
	width, space := 3, 26*26*26
	for n >= space {
		n -= space
		width++
		space *= 26
	}
	v := (n*7919 + 4099) % space // 7919 is coprime with 26
	b := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		b[i] = byte('a' + v%26)
		v /= 26
	}
	return string(b)
}

func (l *lineLabels) mintN(n int) []string {
	tags := make([]string, n)
	for i := range tags {
		tags[i] = l.mint()
	}
	return tags
}

func (l *lineLabels) retire(tags []string, at int) {
	for _, tag := range tags {
		l.retired[tag] = at
	}
}

func (l *lineLabels) replaceAll(text string) {
	l.retire(l.tags, 0)
	lines, _ := splitLines(text)
	l.set(text, l.mintN(len(lines)))
}

// splice records an edit Files made itself: cut lines at index at became n new ones.
func (l *lineLabels) splice(text string, at, cut, n int) {
	l.retire(l.tags[at:at+cut], at)
	l.set(text, slices.Concat(l.tags[:at], l.mintN(n), l.tags[at+cut:]))
}

func (l *lineLabels) reconcile(text string) {
	before, _ := splitLines(l.text)
	after, _ := splitLines(text)
	match := matchLines(before, after)
	tags := make([]string, len(after))
	next := make([]int, len(before)) // for each kept old line, its new index; -1 when changed
	for i := range next {
		next[i] = -1
	}
	for i, j := range match {
		if j < 0 {
			tags[i] = l.mint()
			continue
		}
		tags[i], next[j] = l.tags[j], i
	}
	pos := 0
	for j, i := range next {
		if i >= 0 {
			pos = i + 1
			continue
		}
		l.retired[l.tags[j]] = pos
	}
	l.set(text, tags)
}

// Past this many cells the middle of a change is relabelled wholesale rather
// than aligned. Edits that large are rewrites; nothing useful survives them.
const maxDiffCells = 1 << 21

// matchLines aligns b against a by longest common subsequence, returning for
// each line of b the index of the identical line of a it continues, or -1.
func matchLines(a, b []string) []int {
	match := make([]int, len(b))
	for i := range match {
		match[i] = -1
	}
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		match[p] = p
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		match[len(b)-1-s] = len(a) - 1 - s
		s++
	}
	ma, mb := a[p:len(a)-s], b[p:len(b)-s]
	n, m := len(ma), len(mb)
	if n == 0 || m == 0 || n*m > maxDiffCells {
		return match
	}
	// t[i*w+j] is the LCS length of ma[i:] and mb[j:], so the walk runs forward.
	w := m + 1
	t := make([]int32, (n+1)*w)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if ma[i] == mb[j] {
				t[i*w+j] = t[(i+1)*w+j+1] + 1
			} else {
				t[i*w+j] = max(t[(i+1)*w+j], t[i*w+j+1])
			}
		}
	}
	for i, j := 0, 0; i < n && j < m; {
		switch {
		case ma[i] == mb[j]:
			match[p+j] = p + i
			i++
			j++
		case t[(i+1)*w+j] >= t[i*w+j+1]:
			i++
		default:
			j++
		}
	}
	return match
}

// A label is accepted with or without its line number, in any case, and with
// anything the model copied after the separator. Only the letters identify the
// line; the number is for the reader and may be out of date.
var labelPattern = regexp.MustCompile(`(?s)^\s*(?:(\d+)\s*:\s*)?([A-Za-z]{3,})\s*(?:│.*)?$`)

func (l *lineLabels) locate(field, label string, lines []string) (int, error) {
	m := labelPattern.FindStringSubmatch(label)
	if m == nil {
		return 0, fmt.Errorf("%s %q is not a line label; pass NUMBER:LABEL exactly as read_file shows it, for example 1:%s", field, label, l.tags[0])
	}
	tag := strings.ToLower(m[2])
	if i, ok := l.index[tag]; ok {
		return i, nil
	}
	reason := "is stale: that line changed or was removed after it was shown"
	near, retired := l.retired[tag]
	if !retired {
		if m[1] == "" {
			return 0, fmt.Errorf("%s label %s was never shown for this file; read_file it and use a label from the output", field, label)
		}
		reason = "was never shown for this file"
		n, _ := strconv.Atoi(m[1])
		near = n - 1
	}
	near = min(max(near, 0), len(lines)-1)
	from, to := max(0, near-4), min(len(lines), near+6)
	return 0, fmt.Errorf("%s label %s %s. Current lines near it:\n%s", field, label, reason,
		bounded(lines[from:to], 10, func(i int) string { return labelPrefix(from+i, l.tags[from+i]) }))
}

func labelPrefix(i int, tag string) string { return fmt.Sprintf("%d:%s│", i+1, tag) }

// bounded renders lines with prefixes, keeping the first and last halves of
// limit when there are more.
func bounded(lines []string, limit int, prefix func(int) string) string {
	var b strings.Builder
	for i, line := range lines {
		if len(lines) > limit && i == limit/2 {
			skipped := len(lines) - limit
			fmt.Fprintf(&b, "… %d lines not shown; read_file to see them\n", skipped)
		}
		if len(lines) > limit && i >= limit/2 && i < len(lines)-limit/2 {
			continue
		}
		b.WriteString(prefix(i) + line + "\n")
	}
	return b.String()
}

// splitLines drops the empty element after a final newline, matching how
// read_file numbers lines.
func splitLines(text string) ([]string, bool) {
	lines := strings.Split(text, "\n")
	trailing := lines[len(lines)-1] == ""
	if trailing {
		lines = lines[:len(lines)-1]
	}
	return lines, trailing && len(lines) > 0
}

func joinLines(lines []string, trailing bool) string {
	if len(lines) == 0 {
		return ""
	}
	text := strings.Join(lines, "\n")
	if trailing {
		text += "\n"
	}
	return text
}

// newLines splits replacement text into lines, following a file whose every
// line ends in CRLF.
func newLines(text string, existing []string) []string {
	if text == "" {
		return nil
	}
	added := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for _, line := range existing {
		if !strings.HasSuffix(line, "\r") {
			return added
		}
	}
	for i, line := range added {
		if !strings.HasSuffix(line, "\r") {
			added[i] = line + "\r"
		}
	}
	return added
}
