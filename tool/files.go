package tool

import (
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

type FilesConfig struct {
	// OnChange runs after a committed write and after releasing the file lock.
	// It must return promptly and support concurrent calls.
	OnChange func(path string)
	// Dir is the base for relative paths; absolute paths are used directly.
	Dir string
	// Defaults: files up to 1 MiB and read output up to 64 KiB.
	MaxFileBytes int
	OutputLimit  int
}

// Files serializes its own operations across agents, including read-modify-write
// edits. Shell commands and external editors do not participate in this lock.
// Paths may be absolute or relative to Dir. Symlinks resolve to their targets
// before atomic replacement. These checks do not prevent another process from
// concurrently replacing filesystem entries.
type Files struct {
	config FilesConfig
	gate   chan struct{}
	tools  []Tool
}

func NewFiles(config FilesConfig) (*Files, error) {
	var err error
	config.Dir, err = directory(config.Dir)
	if err != nil {
		return nil, err
	}
	config.MaxFileBytes = cmp.Or(config.MaxFileBytes, 1024*1024)
	config.OutputLimit = cmp.Or(config.OutputLimit, 64*1024)
	if config.MaxFileBytes < 1 || config.OutputLimit < 1 {
		return nil, errors.New("file limits must be positive")
	}
	f := &Files{config: config, gate: make(chan struct{}, 1)}
	f.tools = f.buildTools()
	return f, nil
}

type readArgs struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset"`
	Limit  *int   `json:"limit"`
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type editArgs struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

type ReadFileResult struct {
	Path       string `json:"path"`
	Content    string `json:"content"` // 1-based line numbers followed by a tab
	TotalLines int    `json:"total_lines"`
	More       bool   `json:"more"`
	Truncated  bool   `json:"truncated"` // the byte cap cut the requested window
}

type WriteFileResult struct {
	Path         string `json:"path"`
	BytesWritten int    `json:"bytes_written"`
}

type EditFileResult struct {
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
}

// Tools returns adapters sharing this Files instance. Supply this same set to
// agents that share a workspace so their direct file operations use one lock.
func (f *Files) Tools() []Tool { return slices.Clone(f.tools) }

func (f *Files) buildTools() []Tool {
	return []Tool{
		builtin("read_file",
			fmt.Sprintf("Read a UTF-8 text file. Accepts absolute paths; relative paths resolve from %s. Returns numbered lines, starting at offset (1-based, default 1), up to limit (default 200, maximum 2000). Files are limited to %d bytes and output to %d bytes. Symlinks resolve to their targets.", f.config.Dir, f.config.MaxFileBytes, f.config.OutputLimit),
			f.read, Nullable("offset", "start at the beginning"), Nullable("limit", "use the default read limit"), MinLength("path", 1), Minimum("offset", 1), Minimum("limit", 1), Maximum("limit", 2000)),
		builtin("write_file",
			fmt.Sprintf("Create or replace a UTF-8 text file. Accepts absolute paths; relative paths resolve from %s. Content is limited to %d bytes. Content is literal text, including newlines; do not add Markdown fences or shell heredocs. Parent directories must exist. Symlinks resolve to their targets. Replacements are atomic and retain file permissions.", f.config.Dir, f.config.MaxFileBytes),
			f.write, MinLength("path", 1)),
		builtin("edit_file",
			fmt.Sprintf("Replace exactly one occurrence of old with new in a UTF-8 text file. Accepts absolute paths; relative paths resolve from %s. Old must be nonempty and unique; include surrounding text if ambiguous. Symlinks resolve to their targets. The resulting file is limited to %d bytes.", f.config.Dir, f.config.MaxFileBytes),
			f.edit, MinLength("path", 1), MinLength("old", 1)),
	}
}

func (f *Files) lock(ctx context.Context) error {
	select {
	case f.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			f.unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *Files) unlock() { <-f.gate }

func (f *Files) read(ctx context.Context, _ Call, args readArgs) (Result, error) {
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
		return Result{}, err
	}
	text, err := f.readText(ctx, path)
	if err != nil {
		return Result{}, err
	}
	lines := strings.Split(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	start := min(offset-1, len(lines))
	end := start + min(limit, len(lines)-start)
	var output strings.Builder
	truncated := false
	for i := start; i < end; i++ {
		line := fmt.Sprintf("%d\t%s\n", i+1, lines[i])
		remaining := f.config.OutputLimit - output.Len()
		if len(line) > remaining {
			piece := line[:remaining]
			for !utf8.ValidString(piece) {
				piece = piece[:len(piece)-1]
			}
			output.WriteString(piece)
			truncated = true
			break
		}
		output.WriteString(line)
	}
	return JSON(ReadFileResult{Path: args.Path, Content: output.String(), TotalLines: len(lines), More: end < len(lines) || truncated, Truncated: truncated})
}

func (f *Files) write(ctx context.Context, _ Call, args writeArgs) (Result, error) {
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
	if err := f.atomicWriteText(ctx, path, args.Content); err != nil {
		return Result{}, err
	}
	changed = path
	return JSON(WriteFileResult{Path: args.Path, BytesWritten: len(args.Content)})
}

func (f *Files) edit(ctx context.Context, _ Call, args editArgs) (result Result, err error) {
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
			err = &DiagnosticError{Cause: err, Detail: Diagnostic{Kind: "file_edit", Edit: &EditDiagnostic{RequestedPath: args.Path, ResolvedPath: path, Before: text, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(text))), Old: args.Old, New: args.New}}}
		}
	}()
	first := strings.Index(text, args.Old)
	if first < 0 {
		return Result{}, errors.New("old text was not found")
	}
	// Count would miss overlapping matches, such as "aa" in "aaa".
	if first != strings.LastIndex(text, args.Old) {
		return Result{}, errors.New("old text matches more than once; include more surrounding text")
	}
	if len(args.New) > f.config.MaxFileBytes-(len(text)-len(args.Old)) {
		return Result{}, errors.New("edited file exceeds the byte limit")
	}
	updated := strings.Replace(text, args.Old, args.New, 1)
	if err := f.atomicWriteText(ctx, path, updated); err != nil {
		return Result{}, err
	}
	changed = path
	return JSON(EditFileResult{Path: args.Path, Replacements: 1})
}

func directory(dir string) (string, error) {
	if dir == "" {
		dir = "."
	}
	path, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("working directory must be a directory")
	}
	return path, nil
}

func (f *Files) resolve(path string) (string, error) { return resolveFile(f.config.Dir, path) }

func resolveFile(dir, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("path must name a file")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	// Resolve the entire existing path so atomic writes replace the target file,
	// not a symlink referring to it.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		// Only a missing leaf may be created. A dangling symlink is an existing
		// leaf: return its resolution error rather than replacing the link.
		if _, leafErr := os.Lstat(path); !errors.Is(leafErr, os.ErrNotExist) {
			return "", err
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			return "", err
		}
		info, err := os.Stat(parent)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", errors.New("parent component is not a directory")
		}
		return filepath.Join(parent, filepath.Base(path)), nil
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("path must name a regular file")
	}
	return resolved, nil
}

func (f *Files) readText(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > int64(f.config.MaxFileBytes) {
		return "", errors.New("file must be regular and within the byte limit")
	}
	var text strings.Builder
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := file.Read(buffer)
		if n > f.config.MaxFileBytes-text.Len() {
			return "", errors.New("file exceeds the byte limit")
		}
		text.Write(buffer[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	if err := f.validateText(text.String()); err != nil {
		return "", err
	}
	return text.String(), nil
}

func (f *Files) validateText(text string) error {
	if len(text) > f.config.MaxFileBytes {
		return errors.New("file exceeds the byte limit")
	}
	if !utf8.ValidString(text) || strings.ContainsRune(text, '\x00') {
		return errors.New("file tools require UTF-8 text without NUL bytes")
	}
	return nil
}

// A same-directory rename publishes complete contents. This preserves regular
// permission bits, not inode identity, ownership, extended attributes or hard links.
func (f *Files) atomicWriteText(ctx context.Context, path, content string) error {
	if err := f.validateText(content); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	mode := os.FileMode(0600)
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("path must name a regular file")
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".strap-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tmp.WriteString(content); err != nil {
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
