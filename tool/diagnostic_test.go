package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestEditFailureKeepsExactSearchSnapshotAndOriginalError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := NewFiles(FilesConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.edit(context.Background(), Call{}, editArgs{Path: "file", Old: "missing", New: "replacement"})
	if err == nil || err.Error() != "old text was not found" {
		t.Fatal(err)
	}
	wrapped := fmt.Errorf("wrapper: %w", err)
	resolved, resolveErr := filepath.EvalSymlinks(path)
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	detail := DiagnosticFrom(wrapped)
	if detail == nil || detail.Edit.Before != "original\n" || detail.Edit.Old != "missing" || detail.Edit.ResolvedPath != resolved || detail.Edit.SHA256 == "" {
		t.Fatal(detail)
	}
	var typed *DiagnosticError
	if !errors.As(wrapped, &typed) || !errors.Is(wrapped, typed.Cause) {
		t.Fatal("error chain lost")
	}
	if err := os.WriteFile(path, []byte("later edit"), 0600); err != nil {
		t.Fatal(err)
	}
	detail.Edit.Before = "observer mutation"
	if DiagnosticFrom(wrapped).Edit.Before != "original\n" {
		t.Fatal("diagnostic aliased observer or disk")
	}
}
