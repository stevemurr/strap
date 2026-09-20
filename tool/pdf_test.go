package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func requirePoppler(t *testing.T) {
	t.Helper()
	for _, exe := range []string{"pdfinfo", "pdftoppm"} {
		if _, err := exec.LookPath(exe); err != nil {
			t.Skip("Poppler required: " + exe)
		}
	}
}
func TestPDFRenderingPageOrderAndLimits(t *testing.T) {
	requirePoppler(t)
	pdf, err := NewPDF(PDFConfig{Dir: "testdata", MaxPages: 2, MaxDimension: 400})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		raw   string
		pages []int
	}{{`{"input":{"path":"pages.pdf","pages":null}}`, []int{1, 2}}, {`{"input":{"path":"pages.pdf","pages":[3,1]}}`, []int{3, 1}}} {
		result, err := pdf.Call(context.Background(), Call{Arguments: json.RawMessage(tc.raw)})
		if err != nil {
			t.Fatal(err)
		}
		var metadata PDFMetadata
		if err := json.Unmarshal([]byte(result.Content[0].Text), &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.TotalPages != 3 || metadata.OmittedPages != 1 || !reflect.DeepEqual(metadata.Pages, tc.pages) {
			t.Fatalf("metadata: %+v", metadata)
		}
		if len(result.Content) != 5 {
			t.Fatal("missing page images/labels")
		}
		for i, page := range tc.pages {
			label, img := result.Content[1+2*i], result.Content[2+2*i].Image
			if img == nil || img.MIMEType != "image/png" || !strings.Contains(label.Text, "page "+string(rune('0'+page))) {
				t.Fatal("page attribution lost")
			}
			decoded, err := png.Decode(bytes.NewReader(img.Data))
			if err != nil || decoded.Bounds().Dx() != 400 || decoded.Bounds().Dy() != 300 {
				t.Fatalf("render: %v", err)
			}
		}
	}
	for _, raw := range []string{`{"input":{"path":"pages.pdf","pages":[]}}`, `{"input":{"path":"pages.pdf","pages":[0]}}`, `{"input":{"path":"pages.pdf","pages":[4]}}`, `{"input":{"path":"pages.pdf","pages":[1,1]}}`, `{"input":{"path":"pages.pdf","pages":[1,2,3]}}`} {
		if _, err := pdf.Call(context.Background(), Call{Arguments: json.RawMessage(raw)}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	tiny, _ := NewPDF(PDFConfig{Dir: "testdata", MaxImageBytes: 10})
	if _, err := tiny.Call(context.Background(), Call{Arguments: json.RawMessage(`{"input":{"path":"pages.pdf","pages":null}}`)}); err == nil {
		t.Fatal("image byte cap ignored")
	}
	small, _ := NewPDF(PDFConfig{Dir: "testdata", MaxFileBytes: 10})
	if _, err := small.Call(context.Background(), Call{Arguments: json.RawMessage(`{"input":{"path":"pages.pdf","pages":null}}`)}); err == nil {
		t.Fatal("file byte cap ignored")
	}
}
func TestPDFInvalidFileAndCancellation(t *testing.T) {
	requirePoppler(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "invalid.pdf"), []byte("not a PDF"), 0600); err != nil {
		t.Fatal(err)
	}
	pdf, _ := NewPDF(PDFConfig{Dir: dir})
	if _, err := pdf.Call(context.Background(), Call{Arguments: json.RawMessage(`{"input":{"path":"invalid.pdf","pages":null}}`)}); err == nil {
		t.Fatal("invalid PDF accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pdf.Call(ctx, Call{Arguments: json.RawMessage(`{"input":{"path":"invalid.pdf","pages":null}}`)}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestPDFProcessCancellationAndMissingDependency(t *testing.T) {
	if !processGroupsSupported {
		t.Skip("shell fixture requires Unix")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pdfinfo"), []byte("#!/bin/sh\nexec /bin/sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := pdfCommand(ctx, 1024, "pdfinfo"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := pdfCommand(context.Background(), 1024, "pdftoppm"); err == nil || !strings.Contains(err.Error(), "install Poppler") {
		t.Fatal(err)
	}
}

func TestPDFPathsUseWorkingDirectoryOnlyForRelativePaths(t *testing.T) {
	requirePoppler(t)
	dir := t.TempDir()
	work := filepath.Join(dir, "working")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("testdata/pages.pdf")
	if err != nil {
		t.Fatal(err)
	}
	absolute := filepath.Join(dir, "outside.pdf")
	for _, path := range []string{absolute, filepath.Join(work, "inside.pdf")} {
		if err := os.WriteFile(path, source, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(absolute, filepath.Join(work, "link.pdf")); err != nil {
		t.Fatal(err)
	}
	pdf, err := NewPDF(PDFConfig{Dir: work, MaxPages: 1, MaxDimension: 64})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{absolute, "inside.pdf", "../outside.pdf", "link.pdf"} {
		t.Run(path, func(t *testing.T) {
			args, _ := MarshalInput(map[string]any{"path": path, "pages": nil})
			result, err := pdf.Call(context.Background(), Call{Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			var metadata PDFMetadata
			if err := json.Unmarshal([]byte(result.Content[0].Text), &metadata); err != nil {
				t.Fatal(err)
			}
			if metadata.Path != path || metadata.TotalPages != 3 || len(result.Content) != 3 {
				t.Fatal(metadata)
			}
		})
	}
	for _, path := range []string{"", work, filepath.Join(dir, "missing.pdf")} {
		args, _ := MarshalInput(map[string]any{"path": path, "pages": nil})
		if _, err := pdf.Call(context.Background(), Call{Arguments: args}); err == nil {
			t.Fatalf("accepted invalid file %q", path)
		}
	}
}
