package eval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMountsRejectOverlapAfterResolvingSymlinks(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Fatal(err)
	}
	for _, results := range []string{alias, root} {
		_, err := (Mounts{Workspace: workspace, Results: results, Problems: t.TempDir(), Outbox: t.TempDir()}).resolve(false)
		if err == nil || !strings.Contains(err.Error(), "separate directories") {
			t.Fatal(err)
		}
	}
}

func TestFailedSnapshotNeverPublishes(t *testing.T) {
	mounts := Mounts{Workspace: t.TempDir(), Outbox: t.TempDir()}
	if err := os.WriteFile(filepath.Join(mounts.Workspace, "a.go"), []byte("package probe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.go", filepath.Join(mounts.Workspace, "z.go")); err != nil {
		t.Fatal(err)
	}
	err := publishSubmission(context.Background(), mounts, RunInfo{}, Result{})
	if err == nil || !strings.Contains(err.Error(), "only regular files") {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(mounts.Outbox)
	if err != nil || len(entries) != 0 {
		t.Fatal("partial submission visible", entries, err)
	}
}
