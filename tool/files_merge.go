package tool

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// Under EditMerge the model quotes the text it replaces, as with EditText, and
// the quote is applied in stages: exactly; then ignoring whitespace; then, when
// the quote is close to exactly one place, as a three-way merge of the model's
// change (old -> new) into the real lines there. The merge keeps the real text
// of lines the model misremembered but did not change and refuses when the
// model changed a line it misremembered. Every refusal shows the real lines to
// copy from.
//
// Replaying 227 recorded Qwen edits, the loose stages rescued half of the
// failed quotes; replacing a fuzzy region with new instead would have
// overwritten real lines in 11 of 27 applications.

func (f *Files) mergeTools() []Tool {
	return []Tool{
		builtin("read_file",
			fmt.Sprintf("Read a UTF-8 text file. Accepts absolute paths; relative paths resolve from %s. Returns the file's text exactly as stored, starting at offset (1-based line, default 1), up to limit lines (default 200, maximum 2000). Bracketed header and footer lines are not file content. Files are limited to %d bytes and output to %d bytes. Symlinks resolve to their targets.", f.config.Dir, f.config.MaxFileBytes, f.config.OutputLimit),
			f.readPlain, Nullable("offset", "start at the beginning"), Nullable("limit", "use the default read limit"), MinLength("path", 1), Minimum("offset", 1), Minimum("limit", 1), Maximum("limit", 2000)),
		builtin("write_file",
			fmt.Sprintf("Create or replace a UTF-8 text file. Accepts absolute paths; relative paths resolve from %s. Content is limited to %d bytes. Content is literal text, including newlines; do not add Markdown fences or shell heredocs. Missing parent directories are created. Symlinks resolve to their targets. Replacements are atomic and retain file permissions.", f.config.Dir, f.config.MaxFileBytes),
			f.write, MinLength("path", 1)),
		builtin("edit_file",
			fmt.Sprintf("Replace one occurrence of old with new in a UTF-8 text file. Copy old from the file as read_file shows it, with enough surrounding lines to identify one place. An exact match is used when there is one. Otherwise whitespace differences are tolerated, and when old is close to exactly one place your change is merged into the real lines there, keeping the real text of lines you did not change. Edits that are ambiguous or that change lines which read differently in the file are refused with the real lines to copy from. The result shows the edited lines. Accepts absolute paths; relative paths resolve from %s. The resulting file is limited to %d bytes.", f.config.Dir, f.config.MaxFileBytes),
			f.editMerge, MinLength("path", 1), MinLength("old", 1)),
	}
}

func (f *Files) readPlain(ctx context.Context, _ Call, args readArgs) (Result, error) {
	offset, limit := 1, 200
	if args.Offset != nil {
		offset = *args.Offset
	}
	if args.Limit != nil {
		limit = *args.Limit
	}
	if err := f.lock(ctx); err != nil {
		return Result{}, err
	}
	defer f.unlock()
	path, err := f.resolve(args.Path)
	if err != nil {
		return Result{}, missing(err, args.Path)
	}
	text, err := f.readText(ctx, path)
	if err != nil {
		return Result{}, missing(err, args.Path)
	}
	lines, _ := splitLines(text)
	if len(lines) == 0 {
		return Text(fmt.Sprintf("[%s is empty]\n", args.Path)), nil
	}
	start := offset - 1
	if start >= len(lines) {
		return Text(fmt.Sprintf("[%s has %d lines; offset %d is past the end]\n", args.Path, len(lines), offset)), nil
	}
	end := min(len(lines), start+limit)
	var body strings.Builder
	shown := start
	for i := start; i < end; i++ {
		if body.Len()+len(lines[i])+1 > f.config.OutputLimit {
			break
		}
		body.WriteString(lines[i] + "\n")
		shown = i + 1
	}
	footer := ""
	if shown == start {
		// One line longer than the output limit: show its start rather than nothing.
		piece := lines[start][:f.config.OutputLimit]
		for !utf8.ValidString(piece) {
			piece = piece[:len(piece)-1]
		}
		body.WriteString(piece + "\n")
		shown = start + 1
		footer = fmt.Sprintf("[line %d is cut at the output limit]\n", start+1)
	}
	if shown < len(lines) {
		footer += fmt.Sprintf("[lines %d-%d not shown; read again with offset %d]\n", shown+1, len(lines), shown+1)
	}
	return Text(fmt.Sprintf("[%s: lines %d-%d of %d]\n", args.Path, start+1, shown, len(lines)) + body.String() + footer), nil
}

func (f *Files) editMerge(ctx context.Context, _ Call, args editArgs) (result Result, err error) {
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
	stage := "exact"
	defer func() {
		if err != nil {
			err = &DiagnosticError{Cause: err, Detail: Diagnostic{Kind: "file_edit", Edit: &EditDiagnostic{RequestedPath: args.Path, ResolvedPath: path, Before: text, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(text))), Old: args.Old, New: args.New, Stage: stage}}}
		}
	}()
	commit := func(updated string, at, n int, how string) (Result, error) {
		if err := f.atomicWriteText(ctx, path, updated); err != nil {
			return Result{}, err
		}
		changed = path
		lines := splitKeep(updated)
		from, to := max(0, at-2), min(len(lines), at+n+2)
		return Text(fmt.Sprintf("[edited %s (%s); lines %d-%d now read]\n", args.Path, how, from+1, to) + bounded(lines[from:to], 40, noPrefix)), nil
	}

	if first := strings.Index(text, args.Old); first >= 0 {
		if first != strings.LastIndex(text, args.Old) {
			return Result{}, fmt.Errorf("old text matches %s in %s; include more surrounding lines so it matches one", occurrences(text, args.Old), args.Path)
		}
		at := strings.Count(text[:first], "\n")
		return commit(text[:first]+args.New+text[first+len(args.Old):], at, blockLines(args.New), "exact")
	}

	lines, trailing := splitLines(text)
	crlf := len(lines) > 0
	for _, line := range lines {
		crlf = crlf && strings.HasSuffix(line, "\r")
	}
	body := slices.Clone(lines)
	if crlf {
		for i := range body {
			body[i] = strings.TrimSuffix(body[i], "\r")
		}
	}
	old, repl := splitBlock(args.Old), splitBlock(args.New)
	join := func(updated []string) string {
		if crlf {
			for i := range updated {
				updated[i] += "\r"
			}
		}
		return joinLines(updated, trailing || len(lines) == 0)
	}

	stage = "whitespace"
	spans := tolerantSpans(body, old)
	if len(spans) > 1 {
		return Result{}, fmt.Errorf("old text does not match %s exactly, and ignoring whitespace it matches %d places (lines %s); include more surrounding lines", args.Path, len(spans), spanList(spans))
	}
	if len(spans) == 1 {
		a, z := spans[0][0], spans[0][1]
		adjusted := reindent(pairsTolerant(old, body, a), trimBlankEdges(old, repl))
		updated := slices.Concat(body[:a], adjusted, body[z+1:])
		return commit(join(updated), a, len(adjusted), "matched ignoring whitespace")
	}

	stage = "merge"
	loc := locateQuote(body, old)
	switch loc.status {
	case "ambiguous":
		return Result{}, fmt.Errorf("old text was not found in %s, and it is similar to more than one place (lines %s); copy old exactly from the place you mean", args.Path, spanList(loc.rivals))
	case "ok":
		a, z := loc.span[0], loc.span[1]
		region := body[a : z+1]
		merged, fromNew, conflict := merge3(old, region, repl)
		if conflict != nil {
			return Result{}, fmt.Errorf("old text does not match %s exactly, and your edit changes lines that read differently in the file (you quoted %q where the file has %q), so nothing was changed. The file's lines %d-%d are:\n%s", args.Path, conflict.quoted, conflict.real, a+1, z+1, rawLines(body, a, z+1))
		}
		merged = reindentSome(pairsAligned(old, region), merged, fromNew)
		if slices.Equal(merged, region) {
			stage = "already present"
			return Text(fmt.Sprintf("[no change: %s already reads as your edit intends at lines %d-%d]\n", args.Path, a+1, z+1) + bounded(body[a:z+1], 40, noPrefix)), nil
		}
		updated := slices.Concat(body[:a], merged, body[z+1:])
		return commit(join(updated), a, len(merged), "merged into the real lines")
	}

	// Nothing close enough to old: the edit may already have been made.
	if present := tolerantSpans(body, repl); len(present) == 1 && substantial(repl) {
		stage = "already present"
		p := present[0]
		return Text(fmt.Sprintf("[no change: old text was not found, but new is already in %s at lines %d-%d]\n", args.Path, p[0]+1, p[1]+1) + bounded(body[p[0]:p[1]+1], 40, noPrefix)), nil
	}
	stage = "not found"
	if loc.closest[1] > loc.closest[0] || loc.matched > 0 {
		c := loc.closest
		return Result{}, fmt.Errorf("old text was not found in %s. The most similar lines (%d-%d) are:\n%s", args.Path, c[0]+1, c[1]+1, rawLines(body, c[0], c[1]+1))
	}
	return Result{}, fmt.Errorf("old text was not found in %s; read the file and copy old from it", args.Path)
}

func noPrefix(int) string { return "" }

// mergeKey compares lines ignoring all whitespace differences.
func mergeKey(s string) string { return strings.Join(strings.Fields(s), " ") }

func keys(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = mergeKey(line)
	}
	return out
}

// splitBlock splits quoted text into lines, ignoring one final newline.
func splitBlock(text string) []string {
	if text == "" {
		return nil
	}
	text = strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r")
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines
}

func splitKeep(text string) []string { lines, _ := splitLines(text); return lines }

func blockLines(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(text, "\n"), "\n") + 1
}

func rawLines(lines []string, from, to int) string {
	from, to = max(0, from), min(len(lines), to)
	if from >= to {
		return ""
	}
	return bounded(lines[from:to], 40, noPrefix)
}

func occurrences(text, old string) string {
	var at []string
	count := 0
	for i := 0; i <= len(text); {
		j := strings.Index(text[i:], old)
		if j < 0 {
			break
		}
		count++
		if len(at) < 5 {
			at = append(at, fmt.Sprint(strings.Count(text[:i+j], "\n")+1))
		}
		i += j + max(1, len(old))
	}
	if count > len(at) {
		at = append(at, "…")
	}
	return fmt.Sprintf("%d places (starting at lines %s)", count, strings.Join(at, ", "))
}

func spanList(spans [][2]int) string {
	var parts []string
	for i, s := range spans {
		if i == 5 {
			parts = append(parts, "…")
			break
		}
		parts = append(parts, fmt.Sprintf("%d-%d", s[0]+1, s[1]+1))
	}
	return strings.Join(parts, ", ")
}

// substantial rejects replacement text too generic to identify an edit, such
// as a lone closing brace.
func substantial(lines []string) bool {
	n := 0
	for _, line := range lines {
		n += len(strings.Join(strings.Fields(line), ""))
	}
	return n >= 16
}

// tolerantSpans finds every place where the non-blank lines of quote equal
// consecutive non-blank lines of body, ignoring whitespace.
func tolerantSpans(body, quote []string) [][2]int {
	var want []string
	for _, k := range keys(quote) {
		if k != "" {
			want = append(want, k)
		}
	}
	if len(want) == 0 {
		return nil
	}
	var idx []int
	var have []string
	for i, k := range keys(body) {
		if k != "" {
			idx, have = append(idx, i), append(have, k)
		}
	}
	var spans [][2]int
	for s := 0; s+len(want) <= len(have); s++ {
		if slices.Equal(have[s:s+len(want)], want) {
			spans = append(spans, [2]int{idx[s], idx[s+len(want)-1]})
		}
	}
	return spans
}

// pairsTolerant pairs each non-blank quote line with the body line it matched.
func pairsTolerant(quote, body []string, at int) [][2]string {
	var pairs [][2]string
	j := at
	for _, q := range quote {
		if mergeKey(q) == "" {
			continue
		}
		for j < len(body) && mergeKey(body[j]) == "" {
			j++
		}
		if j < len(body) {
			pairs = append(pairs, [2]string{q, body[j]})
			j++
		}
	}
	return pairs
}

// trimBlankEdges drops from repl the blank lines old has at its edges, since
// the matched span starts and ends on non-blank lines.
func trimBlankEdges(old, repl []string) []string {
	lead := 0
	for lead < len(old) && lead < len(repl) && mergeKey(old[lead]) == "" && mergeKey(repl[lead]) == "" {
		lead++
	}
	trail := 0
	for trail < len(old)-lead && trail < len(repl)-lead && mergeKey(old[len(old)-1-trail]) == "" && mergeKey(repl[len(repl)-1-trail]) == "" {
		trail++
	}
	return repl[lead : len(repl)-trail]
}

// indentShift finds one consistent change of leading whitespace between the
// model's quoted lines and the real ones: add a prefix, remove one, or none.
func indentShift(pairs [][2]string) (add, remove string, ok bool) {
	lead := func(s string) string { return s[:len(s)-len(strings.TrimLeft(s, " \t"))] }
	first := true
	for _, p := range pairs {
		q, r := lead(p[0]), lead(p[1])
		var a, rm string
		switch {
		case q == r:
		case strings.HasPrefix(r, q):
			a = r[len(q):]
		case strings.HasPrefix(q, r):
			rm = q[len(r):]
		default:
			return "", "", false
		}
		if first {
			add, remove, first = a, rm, false
		} else if a != add || rm != remove {
			return "", "", false
		}
	}
	return add, remove, !first && (add != "" || remove != "")
}

func shiftLine(line, add, remove string) string {
	if strings.TrimSpace(line) == "" {
		return line
	}
	if remove != "" {
		return strings.TrimPrefix(line, remove)
	}
	return add + line
}

func reindent(pairs [][2]string, lines []string) []string {
	add, remove, ok := indentShift(pairs)
	out := slices.Clone(lines)
	if ok {
		for i := range out {
			out[i] = shiftLine(out[i], add, remove)
		}
	}
	return out
}

func reindentSome(pairs [][2]string, lines []string, from []bool) []string {
	add, remove, ok := indentShift(pairs)
	if ok {
		for i := range lines {
			if from[i] {
				lines[i] = shiftLine(lines[i], add, remove)
			}
		}
	}
	return lines
}

type quoteLocation struct {
	status  string // ok, ambiguous or far
	span    [2]int // inclusive body lines when ok
	closest [2]int // best candidate, for showing the model
	matched int
	rivals  [][2]int
}

// locateQuote finds where a misremembered quote came from: the one place where
// at least 80% of its non-blank lines align, spanning within three lines of the
// quote's length, with no other place aligning as well. Every merge that
// matched the model's intent in the recorded failures aligned 83% or more.
func locateQuote(body, quote []string) quoteLocation {
	qk, bk := keys(quote), keys(body)
	need := 0
	for _, k := range qk {
		if k != "" {
			need++
		}
	}
	if need == 0 || len(body) == 0 {
		return quoteLocation{status: "far"}
	}
	where := map[string][]int{}
	for j, k := range bk {
		if k != "" {
			where[k] = append(where[k], j)
		}
	}
	votes := map[int]int{}
	for i, k := range qk {
		if js := where[k]; k != "" && len(js) <= 32 {
			for _, j := range js {
				votes[j-i]++
			}
		}
	}
	offsets := make([]int, 0, len(votes))
	for o := range votes {
		offsets = append(offsets, o)
	}
	sort.Slice(offsets, func(a, b int) bool {
		if votes[offsets[a]] != votes[offsets[b]] {
			return votes[offsets[a]] > votes[offsets[b]]
		}
		return offsets[a] < offsets[b]
	})
	if len(offsets) > 12 {
		offsets = offsets[:12]
	}
	type candidate struct {
		span    [2]int
		matched int
	}
	best := map[[2]int]int{}
	for _, o := range offsets {
		lo, hi := max(0, o-3), min(len(body), o+len(quote)+3)
		if lo >= hi {
			continue
		}
		match := matchLines(bk[lo:hi], qk)
		first, last, matched := -1, -1, 0
		for i, j := range match {
			if j < 0 || qk[i] == "" {
				continue
			}
			matched++
			if first < 0 {
				first = i
			}
			last = i
		}
		if matched == 0 {
			continue
		}
		span := [2]int{max(0, lo+match[first]-first), min(len(body)-1, lo+match[last]+(len(quote)-1-last))}
		if matched > best[span] {
			best[span] = matched
		}
	}
	var cands []candidate
	for s, m := range best {
		cands = append(cands, candidate{s, m})
	}
	if len(cands) == 0 {
		return quoteLocation{status: "far"}
	}
	sort.Slice(cands, func(a, b int) bool {
		if cands[a].matched != cands[b].matched {
			return cands[a].matched > cands[b].matched
		}
		da := abs(cands[a].span[1] - cands[a].span[0] + 1 - len(quote))
		db := abs(cands[b].span[1] - cands[b].span[0] + 1 - len(quote))
		if da != db {
			return da < db
		}
		return cands[a].span[0] < cands[b].span[0]
	})
	top := cands[0]
	loc := quoteLocation{status: "far", closest: top.span, matched: top.matched}
	if top.matched*5 < need*4 || abs(top.span[1]-top.span[0]+1-len(quote)) > 3 {
		return loc
	}
	for _, c := range cands[1:] {
		if c.matched == top.matched && (c.span[1] < top.span[0] || c.span[0] > top.span[1]) {
			loc.rivals = append(loc.rivals, c.span)
		}
	}
	if len(loc.rivals) > 0 {
		loc.status, loc.rivals = "ambiguous", append([][2]int{top.span}, loc.rivals...)
		return loc
	}
	loc.status, loc.span = "ok", top.span
	return loc
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// pairsAligned pairs quote lines with the region lines they align to.
func pairsAligned(quote, region []string) [][2]string {
	var pairs [][2]string
	for i, j := range matchLines(keys(region), keys(quote)) {
		if j >= 0 && mergeKey(quote[i]) != "" {
			pairs = append(pairs, [2]string{quote[i], region[j]})
		}
	}
	return pairs
}

type mergeConflict struct{ quoted, real string }

// merge3 applies the model's change (base -> theirs) to the real lines (ours).
// Lines are compared ignoring whitespace. A chunk only the model changed takes
// the model's text; a chunk only reality differs in keeps reality; a chunk both
// changed differently is a conflict. fromTheirs marks output lines taken from
// the model, which may need re-indenting to the real file.
func merge3(base, ours, theirs []string) (out []string, fromTheirs []bool, conflict *mergeConflict) {
	bk, ok, tk := keys(base), keys(ours), keys(theirs)
	bo, bt := matchLines(ok, bk), matchLines(tk, bk)
	emit := func(lines []string, model bool) {
		out = append(out, lines...)
		for range lines {
			fromTheirs = append(fromTheirs, model)
		}
	}
	i, j, k := 0, 0, 0
	for {
		b := -1
		for c := i; c < len(base); c++ {
			if bo[c] >= j && bt[c] >= k {
				b = c
				break
			}
		}
		bi, oj, tkk := len(base), len(ours), len(theirs)
		if b >= 0 {
			bi, oj, tkk = b, bo[b], bt[b]
		}
		cb, co, ct := bk[i:bi], ok[j:oj], tk[k:tkk]
		switch {
		case slices.Equal(co, cb):
			emit(theirs[k:tkk], true)
		case slices.Equal(ct, cb):
			emit(ours[j:oj], false)
		case slices.Equal(co, ct):
			emit(ours[j:oj], false)
		default:
			quoted, real := strings.Join(base[i:bi], "\n"), strings.Join(ours[j:oj], "\n")
			return nil, nil, &mergeConflict{quoted: clip(quoted), real: clip(real)}
		}
		if b < 0 {
			return out, fromTheirs, nil
		}
		if theirs[bt[b]] != base[b] {
			emit([]string{theirs[bt[b]]}, true)
		} else {
			emit([]string{ours[bo[b]]}, false)
		}
		i, j, k = b+1, bo[b]+1, bt[b]+1
	}
}

func clip(s string) string {
	if len(s) > 200 {
		s = s[:200]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
		s += "…"
	}
	return s
}
