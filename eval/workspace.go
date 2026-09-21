package eval

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Materialize copies the task workspace into dest, which must not already
// contain files.
func Materialize(task Task, dest string) error {
	if entries, err := os.ReadDir(dest); err == nil && len(entries) > 0 {
		return fmt.Errorf("workspace %s is not empty", dest)
	}
	return copyTree(task.WorkspaceDir(), dest)
}

// ApplyHidden copies the hidden test files over the workspace at dest.
func ApplyHidden(task Task, dest string) error { return copyTree(task.HiddenDir(), dest) }

// ApplyReference copies the reference solution over the workspace at dest,
// replacing the stubs of the same name.
func ApplyReference(task Task, dest string) error { return copyTree(task.ReferenceDir(), dest) }

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: only regular files are copied", path)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm()|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
