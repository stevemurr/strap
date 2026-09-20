package tool

import (
	"context"
	"encoding/json"
	"github.com/stevemurr/strap/provider"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalToolConfigurationErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("text"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, filepath.Join(dir, "missing")} {
		if _, err := NewFiles(FilesConfig{Dir: path}); err == nil {
			t.Fatal("files directory accepted", path)
		}
		if _, err := NewShell(ShellConfig{Dir: path}); err == nil {
			t.Fatal("shell directory accepted", path)
		}
		if _, err := NewPDF(PDFConfig{Dir: path}); err == nil {
			t.Fatal("PDF directory accepted", path)
		}
	}
	for _, c := range []FilesConfig{{Dir: dir, MaxFileBytes: -1}, {Dir: dir, OutputLimit: -1}} {
		if _, err := NewFiles(c); err == nil {
			t.Fatal("invalid file limits")
		}
	}
	for _, c := range []ShellConfig{{Dir: dir, Program: "/missing/shell"}, {Dir: dir, Timeout: time.Nanosecond}, {Dir: dir, Timeout: time.Second, MaxTimeout: time.Millisecond}, {Dir: dir, OutputLimit: 1}} {
		if _, err := NewShell(c); err == nil {
			t.Fatal("invalid shell config")
		}
	}
	for _, c := range []PDFConfig{{Dir: dir, MaxPages: 33}, {Dir: dir, MaxDimension: 1}, {Dir: dir, MaxFileBytes: -1}, {Dir: dir, Timeout: -1}} {
		if _, err := NewPDF(c); err == nil {
			t.Fatal("invalid PDF config")
		}
	}
	t.Setenv("PATH", dir)
	shell, err := NewShell(ShellConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	result, err := shell.Call(context.Background(), Call{Arguments: json.RawMessage(`{"input":{"command":"printf fallback","timeout_ms":null}}`)})
	if err != nil || !strings.Contains(result.Content.Text(), "fallback") {
		t.Fatal(result, err)
	}
}
func TestShellAndPDFCanParticipateInTypedComposition(t *testing.T) {
	dir := t.TempDir()
	shell, err := NewShell(ShellConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	pdf, err := NewPDF(PDFConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	combined, err := Compose(provider.ToolDefinition{Name: "local"}, shell, pdf)
	if err != nil {
		t.Fatal(err)
	}
	nested, err := Compose(provider.ToolDefinition{Name: "nested"}, combined)
	if err != nil {
		t.Fatal(err)
	}
	result, err := nested.Call(context.Background(), Call{Arguments: json.RawMessage(`{"input":{"command":"printf composed","timeout_ms":null}}`)})
	if err != nil || !strings.Contains(result.Content.Text(), "composed") {
		t.Fatal(result, err)
	}
	if _, err := nested.Call(context.Background(), Call{Arguments: json.RawMessage(`{"input":{"path":"missing.pdf","pages":null}}`)}); err == nil {
		t.Fatal("missing PDF succeeded")
	}
	if _, err := pdf.Call(context.Background(), Call{Arguments: json.RawMessage(`{"input":{"path":" ","offset":null,"limit":null}}`)}); err == nil {
		t.Fatal("blank PDF path accepted")
	}
}
func TestPDFRejectsMalformedRendererOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "input.pdf"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	for _, tc := range []struct{ name, info, render, want string }{
		{"missing count", "printf 'Title: document\\n'", "", "valid page count"},
		{"invalid count", "printf 'Pages: invalid\\n'", "", "valid page count"},
		{"invalid image", "printf 'Pages: 1\\n'", "printf 'not png'", "invalid or oversized PNG"},
		{"large diagnostic", "printf '%070000d' 0", "", "byte limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for name, script := range map[string]string{"pdfinfo": tc.info, "pdftoppm": tc.render} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			pdf, err := NewPDF(PDFConfig{Dir: dir})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pdf.Call(context.Background(), Call{Arguments: json.RawMessage(`{"input":{"path":"input.pdf","pages":null}}`)}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal(err)
			}
		})
	}
}
func TestLocalToolsReportFilesystemErrorsWithoutChangingFiles(t *testing.T) {
	dir := t.TempDir()
	_, kit := fileTools(t, FilesConfig{Dir: dir})
	file := filepath.Join(dir, "locked")
	if err := os.WriteFile(file, []byte("original"), 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(file, 0600) })
	for _, name := range []string{"read_file", "edit_file"} {
		raw := `{"input":{"path":"locked","offset":null,"limit":null}}`
		if name == "edit_file" {
			raw = `{"input":{"path":"locked","old":"original","new":"changed"}}`
		}
		if _, err := kit[name].Call(context.Background(), Call{Arguments: json.RawMessage(raw)}); err == nil {
			t.Fatal("unreadable file accepted")
		}
	}
	loop := filepath.Join(dir, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"input":{"path":"loop","offset":null,"limit":null}}`, `{"input":{"path":"locked/child","offset":null,"limit":null}}`} {
		if _, err := kit["read_file"].Call(context.Background(), Call{Arguments: json.RawMessage(raw)}); err == nil {
			t.Fatal("invalid path accepted")
		}
	}
	os.Chmod(dir, 0500)
	defer os.Chmod(dir, 0700)
	if _, err := kit["write_file"].Call(context.Background(), Call{Arguments: json.RawMessage(`{"input":{"path":"new","content":"x"}}`)}); err == nil {
		t.Fatal("write in read-only directory succeeded")
	}
}

func TestLocalDefinitionsAndDefaultDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	shell, err := NewShell(ShellConfig{})
	if err != nil {
		t.Fatal(err)
	}
	pdf, err := NewPDF(PDFConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []interface {
		Definition() provider.ToolDefinition
		Validate() error
	}{shell, pdf} {
		if err := op.Validate(); err != nil {
			t.Fatal(err)
		}
		if !json.Valid(op.Definition().Parameters) {
			t.Fatal("invalid schema")
		}
	}
	_, kit := fileTools(t, FilesConfig{OutputLimit: 3})
	callJSON(t, kit["write_file"], map[string]any{"path": "unicode", "content": "世界"}, nil)
	var got ReadFileResult
	callJSON(t, kit["read_file"], map[string]any{"path": "unicode", "offset": nil, "limit": nil}, &got)
	if got.Content != "1\t" || !got.Truncated {
		t.Fatal(got)
	}
	for _, name := range []string{"read_file", "edit_file"} {
		raw := `{"input":{"path":" ","offset":null,"limit":null}}`
		if name == "edit_file" {
			raw = `{"input":{"path":" ","old":"x","new":"y"}}`
		}
		if _, err := kit[name].Call(context.Background(), Call{Arguments: json.RawMessage(raw)}); err == nil {
			t.Fatal("blank path accepted")
		}
	}
}
func TestPDFSnapshotCreationAndReadFailures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.pdf")
	if err := os.WriteFile(path, []byte("fixture"), 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0600)
	p, err := NewPDF(PDFConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Call(context.Background(), Call{Arguments: json.RawMessage(`{"input":{"path":"input.pdf","pages":null}}`)}); err == nil {
		t.Fatal("unreadable input accepted")
	}
	os.Chmod(path, 0600)
	t.Setenv("TMPDIR", filepath.Join(dir, "missing"))
	if _, err := p.Call(context.Background(), Call{Arguments: json.RawMessage(`{"input":{"path":"input.pdf","pages":null}}`)}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatal(err)
	}
}
