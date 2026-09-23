package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mergeFile(t *testing.T, name, content string) (string, map[string]Tool) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, kit := fileTools(t, FilesConfig{Dir: dir, Edits: EditMerge})
	return dir, kit
}

func callText(t *testing.T, operation Tool, args any) string {
	t.Helper()
	raw, err := MarshalInput(args)
	if err != nil {
		t.Fatal(err)
	}
	out, err := operation.Call(context.Background(), Call{Arguments: raw})
	if err != nil {
		t.Fatal(err)
	}
	return out.Content.Text()
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const mergeSrc = `package lfu

// Get returns the value for key.
func (c *Cache) Get(key string) (string, bool) {
	elem, ok := c.entries[key]
	if !ok {
		return "", false
	}
	node := elem.Value.(*entry)
	c.touch(node, elem)
	return node.value, true
}
`

func TestMergeToolsValidate(t *testing.T) {
	_, kit := fileTools(t, FilesConfig{Dir: t.TempDir(), Edits: EditMerge})
	for name, operation := range kit {
		if err := ValidateTool(operation); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if !strings.Contains(string(kit["edit_file"].Definition().Parameters), `"old"`) {
		t.Fatal("merge mode must keep the old/new contract")
	}
}

func TestMergeReadIsPlainText(t *testing.T) {
	_, kit := mergeFile(t, "c.go", mergeSrc)
	got := callText(t, kit["read_file"], map[string]any{"path": "c.go", "offset": 4, "limit": 2})
	want := "[c.go: lines 4-5 of 12]\nfunc (c *Cache) Get(key string) (string, bool) {\n\telem, ok := c.entries[key]\n[lines 6-12 not shown; read again with offset 6]\n"
	if got != want {
		t.Fatalf("read:\n%q\nwant\n%q", got, want)
	}
}

func TestMergeStages(t *testing.T) {
	for _, c := range []struct {
		name, old, new, want, stage string
	}{
		{"exact", "\tc.touch(node, elem)\n", "\tc.touch(node, elem)\n\tc.hits++\n",
			strings.Replace(mergeSrc, "\tc.touch(node, elem)\n", "\tc.touch(node, elem)\n\tc.hits++\n", 1), "(exact)"},
		// The read-view prefix leak: every quoted line carries one extra tab.
		{"indentation", "\t\tif !ok {\n\t\t\treturn \"\", false\n\t\t}", "\t\tif !ok {\n\t\t\tc.misses++\n\t\t\treturn \"\", false\n\t\t}",
			strings.Replace(mergeSrc, "\tif !ok {\n\t\treturn \"\", false\n\t}", "\tif !ok {\n\t\tc.misses++\n\t\treturn \"\", false\n\t}", 1), "(matched ignoring whitespace)"},
		// The model misremembers an unchanged line (entries -> items) and edits
		// another: the change lands and the real line survives.
		{"merge", "\telem, ok := c.items[key]\n\tif !ok {\n\t\treturn \"\", false\n\t}\n\tnode := elem.Value.(*entry)\n\tc.touch(node, elem)",
			"\telem, ok := c.items[key]\n\tif !ok {\n\t\treturn \"\", false\n\t}\n\tnode := elem.Value.(*entry)\n\tc.promote(node, elem)",
			strings.Replace(mergeSrc, "c.touch(node, elem)", "c.promote(node, elem)", 1), "(merged into the real lines)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, kit := mergeFile(t, "c.go", mergeSrc)
			out := callText(t, kit["edit_file"], map[string]any{"path": "c.go", "old": c.old, "new": c.new})
			if got := read(t, dir, "c.go"); got != c.want {
				t.Fatalf("file:\n%s\nwant:\n%s", got, c.want)
			}
			if !strings.Contains(out, c.stage) || strings.Contains(out, `\t`) {
				t.Fatalf("result: %q", out)
			}
		})
	}
}

func TestMergeRefusals(t *testing.T) {
	dup := mergeSrc + "\nfunc (c *Cache) Peek(key string) (string, bool) {\n\telem, ok := c.entries[key]\n\tif !ok {\n\t\treturn \"\", false\n\t}\n\treturn elem.Value.(*entry).value, true\n}\n"
	for _, c := range []struct {
		name, src, old, new, want, stage string
	}{
		{"ambiguous exact", dup, "\tif !ok {\n\t\treturn \"\", false\n\t}", "x", "matches 2 places (starting at lines 6, 16)", "exact"},
		{"ambiguous ignoring whitespace", dup, "if !ok {\nreturn \"\", false\n}", "x", "matches 2 places (lines 6-8, 16-18)", "whitespace"},
		// The model changes the very line it misremembered: refuse and show it.
		{"conflict", mergeSrc, "\telem, ok := c.items[key]\n\tif !ok {\n\t\treturn \"\", false\n\t}\n\tnode := elem.Value.(*entry)",
			"\telem, ok := c.lookup(key)\n\tif !ok {\n\t\treturn \"\", false\n\t}\n\tnode := elem.Value.(*entry)",
			"\telem, ok := c.entries[key]\n\tif !ok {", "merge"},
		{"far", mergeSrc, "\tfor k, v := range c.entries {\n\t\tdelete(c.entries, k)\n\t}", "x", "old text was not found", "not found"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, kit := mergeFile(t, "c.go", c.src)
			raw, _ := MarshalInput(map[string]any{"path": "c.go", "old": c.old, "new": c.new})
			_, err := kit["edit_file"].Call(context.Background(), Call{Arguments: raw})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v, want %q", err, c.want)
			}
			if d := DiagnosticFrom(err); d == nil || d.Edit.Stage != c.stage {
				t.Fatalf("stage: %+v", d)
			}
			if read(t, dir, "c.go") != c.src {
				t.Fatal("a refused edit changed the file")
			}
		})
	}
}

func TestMergeAlreadyPresent(t *testing.T) {
	dir, kit := mergeFile(t, "c.go", mergeSrc)
	// The model re-sends an edit it already made: old is gone, new is there.
	out := callText(t, kit["edit_file"], map[string]any{"path": "c.go", "old": "\tc.bump(node)\n\treturn node.value, true", "new": "\tc.touch(node, elem)\n\treturn node.value, true"})
	if !strings.Contains(out, "no change") || read(t, dir, "c.go") != mergeSrc {
		t.Fatalf("already present: %q", out)
	}
}

func TestMergeKeepsCRLF(t *testing.T) {
	src := strings.ReplaceAll(mergeSrc, "\n", "\r\n")
	dir, kit := mergeFile(t, "c.go", src)
	callText(t, kit["edit_file"], map[string]any{"path": "c.go", "old": "\t\tif !ok {\n\t\t\treturn \"\", false\n\t\t}", "new": "\t\tif !ok {\n\t\t\treturn \"none\", false\n\t\t}"})
	if got := read(t, dir, "c.go"); got != strings.Replace(src, `return "", false`, `return "none", false`, 1) {
		t.Fatalf("crlf: %q", got)
	}
}
