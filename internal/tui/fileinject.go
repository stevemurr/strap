package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxAttachmentFileBytes = 256 << 10
	maxAttachmentBytes     = 1 << 20
	maxAttachmentFiles     = 256
	maxAttachmentEntries   = 10000
	attachmentPrefix       = "\n\n<attached_files>\n"
	attachmentSuffix       = "</attached_files>"
)

type fileMention struct {
	start, end int // byte offsets in the original draft
	path       string
	complete   bool
}

// Mentions start at a word boundary. Quotes allow spaces and punctuation in
// paths. Escaped @, email addresses, and Markdown code spans are literal text.
func fileMentions(text string) []fileMention {
	var refs []fileMention
	for i := 0; i < len(text); {
		if text[i] == '`' {
			n := 1
			for i+n < len(text) && text[i+n] == '`' {
				n++
			}
			if end := strings.Index(text[i+n:], text[i:i+n]); end >= 0 {
				i += n + end + n
			} else {
				i = len(text)
			}
			continue
		}
		if text[i] != '@' {
			_, n := utf8.DecodeRuneInString(text[i:])
			i += n
			continue
		}
		start := i
		i++
		if start > 0 {
			prev, _ := utf8.DecodeLastRuneInString(text[:start])
			if !unicode.IsSpace(prev) && !strings.ContainsRune("([{", prev) {
				continue
			}
		}
		if i < len(text) && (text[i] == '"' || text[i] == '\'') {
			quote := text[i]
			i++
			var path strings.Builder
			for i < len(text) && text[i] != quote && text[i] != '\n' {
				if text[i] == '\\' && i+1 < len(text) && (text[i+1] == quote || text[i+1] == '\\') {
					i++
				}
				path.WriteByte(text[i])
				i++
			}
			complete := i < len(text) && text[i] == quote
			if complete {
				i++
			}
			refs = append(refs, fileMention{start, i, path.String(), complete})
			continue
		}
		begin := i
		for i < len(text) {
			r, n := utf8.DecodeRuneInString(text[i:])
			if unicode.IsSpace(r) || strings.ContainsRune("\"'`<>@", r) {
				break
			}
			i += n
		}
		path := text[begin:i]
		if path != "." && path != ".." {
			path = strings.TrimRight(path, ".,;:!?)]}")
		}
		refs = append(refs, fileMention{start, begin + len(path), path, true})
	}
	return refs
}

func parseFileRefs(text string) []string {
	var refs []string
	seen := map[string]bool{}
	for _, ref := range fileMentions(text) {
		if ref.complete && ref.path != "" && !seen[ref.path] {
			seen[ref.path] = true
			refs = append(refs, ref.path)
		}
	}
	return refs
}

func formatForInjection(content []byte, ref string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### @%s\n\n", ref)
	lines := strings.Split(string(content), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i, line := range lines {
		fmt.Fprintf(&b, "%d\t%s\n", i+1, line)
	}
	return b.String()
}

type attachmentResult struct {
	text                    string
	included, skipped, errs []string
	bytes                   int
}

// Attachments are appended once, without searching or replacing file contents.
// Errors are atomic: the caller must not send a partial attachment set.
func loadAttachments(ctx context.Context, text, cwd string) attachmentResult {
	result := attachmentResult{text: text}
	var body strings.Builder
	seen := map[string]bool{}
	visited := 0
	var visit func(string, string, bool) error
	visit = func(path, label string, explicit bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		path = filepath.Clean(path)
		if seen[path] && !explicit {
			return nil
		}
		visited++
		if visited > maxAttachmentEntries {
			return errors.New("too many folder entries (limit 10000); narrow the folder reference")
		}
		skip := func(reason string) error {
			if explicit {
				return fmt.Errorf("%s: %s", label, reason)
			}
			result.skipped = append(result.skipped, label+": "+reason)
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return skip("symbolic link; reference the target directly")
		}
		if filepath.Base(path) == ".git" {
			return skip("Git metadata")
		}
		if explicit {
			ignored, err := gitIgnored(ctx, filepath.Dir(path), []string{path})
			if err != nil {
				return err
			}
			if ignored[path] {
				return skip("gitignored")
			}
		}
		if info.IsDir() {
			if seen[path] {
				return nil
			}
			seen[path] = true
			// Read at most the traversal budget, even for a huge flat directory.
			f, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("%s: %w", label, err)
			}
			entries, err := f.ReadDir(maxAttachmentEntries - visited + 1)
			f.Close()
			if err != nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("%s: %w", label, err)
			}
			if len(entries) > maxAttachmentEntries-visited {
				return errors.New("too many folder entries (limit 10000); narrow the folder reference")
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
			// Check a directory's children together rather than starting Git for every file.
			paths := make([]string, len(entries))
			for i, entry := range entries {
				paths[i] = filepath.Join(path, entry.Name())
			}
			ignored, err := gitIgnored(ctx, path, paths)
			if err != nil {
				return err
			}
			for i, entry := range entries {
				childLabel := filepath.Join(label, entry.Name())
				if ignored[paths[i]] || entry.Name() == ".git" {
					visited++
					if visited > maxAttachmentEntries {
						return errors.New("too many folder entries (limit 10000); narrow the folder reference")
					}
					result.skipped = append(result.skipped, childLabel+": ignored")
					continue
				}
				if err := visit(paths[i], childLabel, false); err != nil {
					return err
				}
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return skip("not a regular text file")
		}
		if unsupportedAttachment(path) {
			return skip("unsupported format; only text and code are supported")
		}
		if info.Size() > maxAttachmentFileBytes {
			return skip("exceeds 256 KiB per-file limit")
		}
		data, err := readAttachment(path)
		if err != nil {
			return skip(err.Error())
		}
		if seen[path] {
			return nil
		}
		seen[path] = true
		formatted := formatForInjection(data, label)
		if result.bytes+len(data) > maxAttachmentBytes || body.Len()+len(formatted)+1+len(attachmentPrefix)+len(attachmentSuffix) > maxAttachmentBytes {
			return errors.New("attachments exceed 1 MiB per-message limit; narrow the folder or file references")
		}
		if len(result.included) >= maxAttachmentFiles {
			return errors.New("attachments exceed 256 files; narrow the folder reference")
		}
		result.bytes += len(data)
		result.included = append(result.included, label)
		body.WriteString(formatted)
		body.WriteByte('\n')
		return nil
	}
	for _, ref := range fileMentions(text) {
		if !ref.complete {
			result.errs = append(result.errs, "Unclosed quoted @path")
			continue
		}
		if ref.path == "" {
			continue
		}
		path := ref.path
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		if err := visit(path, ref.path, true); err != nil {
			result.errs = append(result.errs, err.Error())
			break
		}
	}
	if err := ctx.Err(); err != nil && len(result.errs) == 0 {
		result.errs = append(result.errs, err.Error())
	}
	if len(result.errs) == 0 && body.Len() > 0 {
		result.text += attachmentPrefix + body.String() + attachmentSuffix
	}
	if len(result.errs) == 0 && len(result.included) == 0 && len(parseFileRefs(text)) > 0 {
		result.errs = append(result.errs, "No eligible text files found in the referenced paths")
	}
	return result
}

func readAttachment(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular text file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxAttachmentFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAttachmentFileBytes {
		return nil, errors.New("exceeds 256 KiB per-file limit")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("not UTF-8 text")
	}
	for _, r := range string(data) {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' && r != '\f' {
			return nil, errors.New("binary content; only text and code are supported")
		}
	}
	if bytes.HasPrefix(data, []byte("%PDF-")) {
		return nil, errors.New("PDF extraction is not supported by @ attachments")
	}
	return data, nil
}

func unsupportedAttachment(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".odt", ".ods", ".odp", ".rtf":
		return true
	}
	return false
}

// Delegate repository ignore semantics (including negation, nested rules,
// global excludes and worktrees) to Git. Outside a repository no rules apply.
func gitIgnored(ctx context.Context, dir string, paths []string) (map[string]bool, error) {
	ignored := map[string]bool{}
	if len(paths) == 0 {
		return ignored, nil
	}
	root := dir
	for {
		if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			return ignored, nil
		}
		root = parent
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "check-ignore", "--no-index", "-z", "--stdin")
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\x00") + "\x00")
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return nil, fmt.Errorf("check Git ignore rules: %w", err)
		}
	}
	for _, path := range strings.Split(string(out), "\x00") {
		if path != "" {
			ignored[path] = true
		}
	}
	return ignored, nil
}
