package tool

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var labelLine = regexp.MustCompile(`^([0-9]+):([a-z]+)│(.*)$`)

// readLabels reads the whole file and returns each line's label, in order.
func readLabels(t *testing.T, kit map[string]Tool, path string) []string {
	t.Helper()
	var read ReadFileResult
	callJSON(t, kit["read_file"], map[string]any{"path": path, "offset": nil, "limit": nil}, &read)
	var labels []string
	for _, line := range strings.Split(strings.TrimSuffix(read.Content, "\n"), "\n") {
		if line == "" {
			continue
		}
		m := labelLine.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("unlabelled line %q in %q", line, read.Content)
		}
		labels = append(labels, m[1]+":"+m[2])
	}
	return labels
}

func callErr(t *testing.T, operation Tool, args any) error {
	t.Helper()
	raw, err := MarshalInput(args)
	if err != nil {
		t.Fatal(err)
	}
	_, err = operation.Call(context.Background(), Call{Arguments: raw})
	if err == nil {
		t.Fatalf("%s %v succeeded", operation.Definition().Name, args)
	}
	return err
}

func edit(op, start string, end any, text string) map[string]any {
	return map[string]any{"path": "f.go", "op": op, "start": start, "end": end, "new": text}
}

func anchoredFile(t *testing.T, content string) (string, map[string]Tool) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, kit := fileTools(t, FilesConfig{Dir: dir, Edits: EditAnchors})
	return dir, kit
}

func contents(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "f.go"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAnchoredToolsValidateAndRejectUnknownMode(t *testing.T) {
	_, kit := fileTools(t, FilesConfig{Dir: t.TempDir(), Edits: EditAnchors})
	for name, operation := range kit {
		if err := ValidateTool(operation); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if !strings.Contains(string(kit["edit_file"].Definition().Parameters), `"start"`) {
		t.Fatalf("edit_file is not the line-label contract: %s", kit["edit_file"].Definition().Parameters)
	}
	if _, err := NewFiles(FilesConfig{Dir: t.TempDir(), Edits: "fuzzy"}); err == nil {
		t.Fatal("unknown edit mode accepted")
	}
}

func TestAnchoredEditsNeverRetypeText(t *testing.T) {
	// Tab indentation, a repeated "}" and a line that differs only in
	// whitespace are each fatal to text matching and irrelevant here.
	dir, kit := anchoredFile(t, "func a() {\n\tx := 1\n}\n\nfunc b() {\n\t\tx := 1\n}\n")
	labels := readLabels(t, kit, "f.go")
	if len(labels) != 7 {
		t.Fatalf("labels: %v", labels)
	}
	var result EditLinesResult
	callJSON(t, kit["edit_file"], edit("replace", labels[5], nil, "\tx := 2"), &result)
	if got := contents(t, dir); got != "func a() {\n\tx := 1\n}\n\nfunc b() {\n\tx := 2\n}\n" {
		t.Fatalf("replace: %q", got)
	}
	if result.Removed != "\t\tx := 1\n" || !strings.Contains(result.View, "│\tx := 2\n") || result.TotalLines != 7 {
		t.Fatalf("result: %+v", result)
	}
	// The second "}" is addressed on its own, with a label read before the
	// previous edit: labels of untouched lines survive edits elsewhere.
	callJSON(t, kit["edit_file"], edit("insert_before", labels[6], nil, "\treturn\n"), nil)
	callJSON(t, kit["edit_file"], edit("insert_after", labels[0], nil, "\t// first"), nil)
	callJSON(t, kit["edit_file"], edit("replace", labels[3], nil, ""), nil)
	if got := contents(t, dir); got != "func a() {\n\t// first\n\tx := 1\n}\nfunc b() {\n\tx := 2\n\treturn\n}\n" {
		t.Fatalf("after inserts and delete: %q", got)
	}
	// A multi-line range, named by labels whose numbers are now out of date.
	callJSON(t, kit["edit_file"], edit("replace", labels[4], labels[6], "func b() {}"), nil)
	if got := contents(t, dir); got != "func a() {\n\t// first\n\tx := 1\n}\nfunc b() {}\n" {
		t.Fatalf("range: %q", got)
	}
}

func TestAnchoredStaleLabelsAreRejectedWithCurrentLines(t *testing.T) {
	dir, kit := anchoredFile(t, "one\ntwo\nthree\n")
	labels := readLabels(t, kit, "f.go")
	callJSON(t, kit["edit_file"], edit("replace", labels[1], nil, "TWO"), nil)
	err := callErr(t, kit["edit_file"], edit("replace", labels[1], nil, "again"))
	fresh := readLabels(t, kit, "f.go")
	if !strings.Contains(err.Error(), "stale") || !strings.Contains(err.Error(), fresh[1]+"│TWO") {
		t.Fatalf("stale error: %v", err)
	}
	if d := DiagnosticFrom(err); d == nil || d.Edit.Op != "replace" || d.Edit.Start != labels[1] || d.Edit.Before != "one\nTWO\nthree\n" {
		t.Fatalf("diagnostic: %+v", d)
	}
	// Changes made outside Files keep labels on unchanged lines only.
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte("zero\none\nTWO\nTHREE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	callJSON(t, kit["edit_file"], edit("replace", fresh[0], nil, "ONE"), nil)
	if err := callErr(t, kit["edit_file"], edit("replace", fresh[2], nil, "x")); !strings.Contains(err.Error(), "stale") {
		t.Fatalf("externally changed line: %v", err)
	}
	if got := contents(t, dir); got != "zero\nONE\nTWO\nTHREE\n" {
		t.Fatalf("contents: %q", got)
	}
}

func TestAnchoredLabelForms(t *testing.T) {
	dir, kit := anchoredFile(t, "a\nb\nc\nd\n")
	labels := readLabels(t, kit, "f.go")
	tag := func(i int) string { return labels[i][strings.Index(labels[i], ":")+1:] }
	callJSON(t, kit["edit_file"], edit("replace", tag(0), nil, "A"), nil)
	callJSON(t, kit["edit_file"], edit("replace", "9:"+strings.ToUpper(tag(1)), nil, "B"), nil)
	callJSON(t, kit["edit_file"], edit("replace", labels[2]+"│c", nil, "C"), nil)
	if got := contents(t, dir); got != "A\nB\nC\nd\n" {
		t.Fatalf("contents: %q", got)
	}
	fresh := readLabels(t, kit, "f.go")
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{edit("replace", "4", nil, "x"), "not a line label"},
		{edit("replace", "4:zzzz", nil, "x"), "never shown"},
		{edit("replace", fresh[3], fresh[0], "x"), "before start"},
		{edit("replace", labels[0], nil, "x"), "stale"},
		{edit("insert_after", fresh[3], fresh[3], "x"), "end must be null"},
		{edit("insert_after", fresh[3], nil, ""), "nothing to insert"},
	} {
		if err := callErr(t, kit["edit_file"], c.args); !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%v: %v", c.args, err)
		}
	}
}

func TestAnchoredLineEndings(t *testing.T) {
	dir, kit := anchoredFile(t, "a\nb")
	labels := readLabels(t, kit, "f.go")
	callJSON(t, kit["edit_file"], edit("insert_after", labels[1], nil, "c\n"), nil)
	if got := contents(t, dir); got != "a\nb\nc" {
		t.Fatalf("no final newline: %q", got)
	}
	callJSON(t, kit["edit_file"], edit("replace", labels[0], readLabels(t, kit, "f.go")[2], ""), nil)
	if got := contents(t, dir); got != "" {
		t.Fatalf("delete all: %q", got)
	}
	if err := callErr(t, kit["edit_file"], edit("replace", labels[0], nil, "x")); !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty file: %v", err)
	}
	dir, kit = anchoredFile(t, "a\r\nb\r\n")
	callJSON(t, kit["edit_file"], edit("insert_after", readLabels(t, kit, "f.go")[0], nil, "x\ny"), nil)
	if got := contents(t, dir); got != "a\r\nx\r\ny\r\nb\r\n" {
		t.Fatalf("crlf: %q", got)
	}
	// A blank replacement line is one empty line; only "" deletes.
	dir, kit = anchoredFile(t, "a\nb\n")
	callJSON(t, kit["edit_file"], edit("replace", readLabels(t, kit, "f.go")[0], nil, "\n"), nil)
	if got := contents(t, dir); got != "\nb\n" {
		t.Fatalf("blank line: %q", got)
	}
}

func TestAnchoredWriteOnlyCreates(t *testing.T) {
	dir, kit := anchoredFile(t, "keep\n")
	if err := callErr(t, kit["write_file"], map[string]any{"path": "f.go", "content": "clobber"}); !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("overwrite: %v", err)
	}
	callJSON(t, kit["write_file"], map[string]any{"path": "new.go", "content": "x\ny\n"}, nil)
	if err := os.WriteFile(filepath.Join(dir, "empty.go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	callJSON(t, kit["write_file"], map[string]any{"path": "empty.go", "content": "z\n"}, nil)
	labels := readLabels(t, kit, "new.go")
	callJSON(t, kit["edit_file"], map[string]any{"path": "new.go", "op": "replace", "start": labels[1], "end": nil, "new": "Y"}, nil)
	if data, _ := os.ReadFile(filepath.Join(dir, "new.go")); string(data) != "x\nY\n" || contents(t, dir) != "keep\n" {
		t.Fatalf("new.go %q, f.go %q", data, contents(t, dir))
	}
}

func TestLineLabelsAreNeverReused(t *testing.T) {
	l := &lineLabels{retired: map[string]int{}}
	seen := map[string]bool{}
	for range 26*26*26 + 1000 {
		tag := l.mint()
		if seen[tag] || !regexp.MustCompile(`^[a-z]{3,4}$`).MatchString(tag) {
			t.Fatalf("label %q after %d", tag, len(seen))
		}
		seen[tag] = true
	}
}

func TestMatchLinesKeepsLongestCommonSubsequence(t *testing.T) {
	a := strings.Split("p a b c d q", " ")
	b := strings.Split("p x b c y d q", " ")
	got := matchLines(a, b)
	want := []int{0, -1, 2, 3, -1, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("match %v, want %v", got, want)
		}
	}
}
