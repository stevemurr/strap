package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func fileTools(t *testing.T, config FilesConfig) (*Files, map[string]Tool) {
	t.Helper()
	f, err := NewFiles(config)
	if err != nil {
		t.Fatal(err)
	}
	kit := make(map[string]Tool)
	for _, operation := range f.Tools() {
		kit[operation.Definition().Name] = operation
	}
	return f, kit
}

func callJSON(t *testing.T, operation Tool, args any, result any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	output, err := operation.Call(context.Background(), Call{Arguments: raw})
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		if err := json.Unmarshal([]byte(output.Content.Text()), result); err != nil {
			t.Fatal(err)
		}
	}
	return output.Content.Text()
}

func TestFilesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	_, kit := fileTools(t, FilesConfig{Dir: dir})
	content := "first\nsecond\nthird\n"
	var written WriteFileResult
	callJSON(t, kit["write_file"], map[string]any{"path": "test.txt", "content": content}, &written)
	if written.BytesWritten != len(content) || written.Path != "test.txt" {
		t.Fatalf("write result: %+v", written)
	}
	var read ReadFileResult
	callJSON(t, kit["read_file"], map[string]any{"path": "test.txt", "offset": 2, "limit": 1}, &read)
	if read.Content != "2\tsecond\n" || !read.More || read.TotalLines != 3 || read.Truncated {
		t.Fatalf("read window: %+v", read)
	}
	callJSON(t, kit["read_file"], map[string]any{"path": "test.txt", "offset": 99}, &read)
	if read.Content != "" || read.More {
		t.Fatalf("past EOF: %+v", read)
	}
	var edited EditFileResult
	callJSON(t, kit["edit_file"], map[string]any{"path": "test.txt", "old": "second", "new": "changed"}, &edited)
	if edited.Replacements != 1 {
		t.Fatalf("edit result: %+v", edited)
	}
	data, err := os.ReadFile(filepath.Join(dir, "test.txt"))
	if err != nil || string(data) != "first\nchanged\nthird\n" {
		t.Fatalf("literal contents: %q, %v", data, err)
	}
	callJSON(t, kit["edit_file"], map[string]any{"path": "test.txt", "old": "changed\n", "new": ""}, nil)
	callJSON(t, kit["write_file"], map[string]any{"path": "test.txt", "content": ""}, nil)
	callJSON(t, kit["read_file"], map[string]any{"path": "test.txt"}, &read)
	if read.TotalLines != 0 || read.Content != "" || read.More {
		t.Fatalf("empty file: %+v", read)
	}
}

func TestFilesInvalidCallsDoNotModify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("same same"), 0600); err != nil {
		t.Fatal(err)
	}
	_, kit := fileTools(t, FilesConfig{Dir: dir})
	cases := []struct{ name, raw string }{
		{"write_file", `{"path":"test.txt"}`},
		{"write_file", `{"path":"test.txt","content":null}`},
		{"write_file", `{"path":"test.txt","content":"bad","extra":true}`},
		{"write_file", `{"path":"test.txt","content":"bad","content":"worse"}`},
		{"write_file", `{"path":"test.txt","content":"bad"} {}`},
		{"write_file", `{"path":"test.txt","content":17}`},
		{"write_file", `null`},
		{"write_file", `[]`},
		{"write_file", `{"path":"test.txt","content":"\u0000"}`},
		{"read_file", `{"path":"test.txt","offset":0}`},
		{"read_file", `{"path":"test.txt","offset":null}`},
		{"read_file", `{"path":"test.txt","limit":2001}`},
		{"read_file", `{"path":"test.txt","limit":1.5}`},
		{"edit_file", `{"path":"test.txt","old":"same","new":"bad"}`},
		{"edit_file", `{"path":"test.txt","old":"missing","new":"bad"}`},
		{"edit_file", `{"path":"test.txt","old":"","new":"bad"}`},
		{"edit_file", `{"path":"test.txt","old":"same same"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name+tc.raw, func(t *testing.T) {
			if _, err := kit[tc.name].Call(context.Background(), Call{Arguments: json.RawMessage(tc.raw)}); err == nil {
				t.Fatal("expected error")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "same same" {
				t.Fatalf("invalid call changed file: %q, %v", data, err)
			}
		})
	}
}

func TestFilesPathsAndSymlinkTargets(t *testing.T) {
	for _, kind := range []string{"relative", "absolute", "parent", "directory_link", "file_link", "link_chain"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			work, outside := filepath.Join(dir, "work"), filepath.Join(dir, "outside")
			for _, path := range []string{work, outside} {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			path, target := "target", filepath.Join(work, "target")
			links := make(map[string]string)
			switch kind {
			case "absolute":
				target = filepath.Join(outside, "target")
				path = target
			case "parent":
				target = filepath.Join(outside, "target")
				path = "../outside/target"
			case "directory_link":
				target = filepath.Join(outside, "target")
				path = "linked/target"
				links[filepath.Join(work, "linked")] = outside
			case "file_link", "link_chain":
				target = filepath.Join(outside, "target")
				path = "linked"
				links[filepath.Join(work, "linked")] = "../outside/target"
				if kind == "link_chain" {
					path = "chain"
					links[filepath.Join(work, "chain")] = "linked"
				}
				if err := os.WriteFile(target, []byte("initial"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for link, dest := range links {
				if err := os.Symlink(dest, link); err != nil {
					t.Fatal(err)
				}
			}
			_, kit := fileTools(t, FilesConfig{Dir: work})
			var written WriteFileResult
			callJSON(t, kit["write_file"], map[string]any{"path": path, "content": "first"}, &written)
			if written.Path != path || written.BytesWritten != 5 {
				t.Fatal(written)
			}
			if err := os.Chmod(target, 0751); err != nil {
				t.Fatal(err)
			}
			// Replacement and editing must both publish to the resolved target.
			callJSON(t, kit["write_file"], map[string]any{"path": path, "content": "second"}, nil)
			var edited EditFileResult
			callJSON(t, kit["edit_file"], map[string]any{"path": path, "old": "second", "new": "final"}, &edited)
			if edited.Path != path || edited.Replacements != 1 {
				t.Fatal(edited)
			}
			var read ReadFileResult
			callJSON(t, kit["read_file"], map[string]any{"path": path}, &read)
			if read.Path != path || read.Content != "1\tfinal\n" {
				t.Fatal(read)
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "final" {
				t.Fatalf("target content %q: %v", data, err)
			}
			info, err := os.Stat(target)
			if err != nil || info.Mode().Perm() != 0751 {
				t.Fatalf("target permissions: %v, %v", info, err)
			}
			for link, dest := range links {
				got, err := os.Readlink(link)
				if err != nil || got != dest {
					t.Fatalf("link replaced: %s -> %s: %v", link, got, err)
				}
			}
		})
	}
}

func TestFilesInvalidPathsDoNotModify(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	for link, dest := range map[string]string{"dangling": "missing", "cycle": "cycle"} {
		if err := os.Symlink(dest, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	_, kit := fileTools(t, FilesConfig{Dir: dir})
	for _, path := range []string{"", ".", dir, "missing/child", "target/child", "dangling", "cycle"} {
		for _, name := range []string{"read_file", "write_file", "edit_file"} {
			args := map[string]any{"path": path}
			if name == "write_file" {
				args["content"] = "changed"
			}
			if name == "edit_file" {
				args["old"], args["new"] = "original", "changed"
			}
			raw, _ := json.Marshal(args)
			if _, err := kit[name].Call(context.Background(), Call{Arguments: raw}); err == nil {
				t.Errorf("%s accepted %q", name, path)
			}
		}
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "original" {
		t.Fatalf("existing file changed: %q, %v", data, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid path created a file: %v", err)
	}
	if dest, err := os.Readlink(filepath.Join(dir, "dangling")); err != nil || dest != "missing" {
		t.Fatal("dangling link replaced")
	}
}

func TestEditRejectsOverlappingMatches(t *testing.T) {
	dir := t.TempDir()
	_, kit := fileTools(t, FilesConfig{Dir: dir})
	callJSON(t, kit["write_file"], map[string]any{"path": "file", "content": "aaa"}, nil)
	if _, err := kit["edit_file"].Call(context.Background(), Call{Arguments: json.RawMessage(`{"path":"file","old":"aa","new":"b"}`)}); err == nil {
		t.Fatal("overlapping matches were considered unique")
	}
	data, err := os.ReadFile(filepath.Join(dir, "file"))
	if err != nil || string(data) != "aaa" {
		t.Fatalf("ambiguous edit changed file: %q, %v", data, err)
	}
}

func TestFilesLimitsAndPermissions(t *testing.T) {
	dir := t.TempDir()
	_, kit := fileTools(t, FilesConfig{Dir: dir, MaxFileBytes: 20, OutputLimit: 8})
	path := filepath.Join(dir, "script")
	if err := os.WriteFile(path, []byte("original"), 0751); err != nil {
		t.Fatal(err)
	}
	// Explicit chmod avoids depending on the caller's umask.
	if err := os.Chmod(path, 0751); err != nil {
		t.Fatal(err)
	}
	callJSON(t, kit["write_file"], map[string]any{"path": "script", "content": "界界界界"}, nil)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0751 {
		t.Fatalf("permissions: %v, %v", info, err)
	}
	var result ReadFileResult
	callJSON(t, kit["read_file"], map[string]any{"path": "script"}, &result)
	if !result.Truncated || !result.More || len(result.Content) > 8 || !utf8.ValidString(result.Content) {
		t.Fatalf("bounded Unicode: %+v", result)
	}
	raw, _ := json.Marshal(map[string]any{"path": "script", "content": strings.Repeat("x", 21)})
	if _, err := kit["write_file"].Call(context.Background(), Call{Arguments: raw}); err == nil {
		t.Fatal("oversized write accepted")
	}
	raw, _ = json.Marshal(map[string]any{"path": "script", "old": "界界界界", "new": strings.Repeat("x", 21)})
	if _, err := kit["edit_file"].Call(context.Background(), Call{Arguments: raw}); err == nil {
		t.Fatal("oversized edit accepted")
	}
	for _, contents := range [][]byte{[]byte(strings.Repeat("x", 21)), {0xff}, {0}} {
		if err := os.WriteFile(path, contents, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := kit["read_file"].Call(context.Background(), Call{Arguments: json.RawMessage(`{"path":"script"}`)}); err == nil {
			t.Fatalf("accepted oversized/binary file %q", contents)
		}
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".strap-write-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary files leaked: %v, %v", leftovers, err)
	}
}

func TestFilesConcurrentEditsAndCancellation(t *testing.T) {
	dir := t.TempDir()
	f, kit := fileTools(t, FilesConfig{Dir: dir})
	callJSON(t, kit["write_file"], map[string]any{"path": "file", "content": "start"}, nil)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := kit["edit_file"].Call(context.Background(), Call{Arguments: json.RawMessage(`{"path":"file","old":"start","new":"done"}`)}); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("%d edits read the same old contents", successes.Load())
	}
	if err := f.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer f.unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := kit["write_file"].Call(ctx, Call{Arguments: json.RawMessage(`{"path":"file","content":"wrong"}`)})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled lock wait: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "file"))
	if err != nil || string(data) != "done" {
		t.Fatalf("canceled call changed file: %q, %v", data, err)
	}
}

func TestFilesAtomicReplacement(t *testing.T) {
	dir := t.TempDir()
	_, kit := fileTools(t, FilesConfig{Dir: dir})
	a, b := strings.Repeat("a", 4096), strings.Repeat("b", 4096)
	callJSON(t, kit["write_file"], map[string]any{"path": "file", "content": a}, nil)
	done := make(chan struct{})
	observed := make(chan error, 1)
	go func() {
		for {
			select {
			case <-done:
				observed <- nil
				return
			default:
			}
			data, err := os.ReadFile(filepath.Join(dir, "file"))
			if err != nil {
				observed <- err
				return
			}
			if string(data) != a && string(data) != b {
				observed <- errors.New("reader saw partial contents")
				return
			}
		}
	}()
	defer func() {
		close(done)
		if err := <-observed; err != nil {
			t.Error(err)
		}
	}()
	for i := 0; i < 40; i++ {
		content := a
		if i%2 == 0 {
			content = b
		}
		callJSON(t, kit["write_file"], map[string]any{"path": "file", "content": content}, nil)
	}
}
