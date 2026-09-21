package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Mounts names the container directories. The CLI always uses
// ContainerMounts; explicit paths support embedding and isolated tests.
type Mounts struct {
	Workspace string `json:"workspace"`
	Results   string `json:"results"`
	Grading   string `json:"grading"`
	Problems  string `json:"problems"`
	Outbox    string `json:"outbox"`
}

func ContainerMounts() Mounts {
	return Mounts{Workspace: "/workspace", Results: "/results", Grading: "/grading", Problems: "/problems", Outbox: "/outbox"}
}

func (m Mounts) resolve(grading bool) (Mounts, error) {
	if m == (Mounts{}) {
		m = ContainerMounts()
	}
	paths := []*string{&m.Workspace, &m.Results, &m.Outbox}
	if grading {
		paths = append(paths, &m.Grading)
	} else {
		paths = append(paths, &m.Problems)
	}
	for _, path := range paths {
		if !filepath.IsAbs(*path) {
			return m, fmt.Errorf("mount path must be absolute: %q", *path)
		}
		resolved, err := filepath.EvalSymlinks(*path)
		if err != nil {
			return m, fmt.Errorf("mount %s: %w", *path, err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return m, err
		}
		if !info.IsDir() {
			return m, fmt.Errorf("mount %s must be a directory", *path)
		}
		*path = resolved
	}
	for i, a := range paths {
		for j, b := range paths {
			if i == j {
				continue
			}
			rel, err := filepath.Rel(*a, *b)
			if err != nil {
				return m, err
			}
			if rel == "." || (rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
				return m, fmt.Errorf("mounts must be separate directories: %s and %s", *a, *b)
			}
		}
	}
	return m, nil
}

func requireEmpty(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("mount %s must be empty; use fresh workspace, results and outbox mounts for each attempt", dir)
	}
	return nil
}
