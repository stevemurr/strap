package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/stevemurr/strap/lsp"
)

// scratchLanguages starts language managers for files written outside the
// workspace, such as an auditor's scratch module in /tmp/verify, one per
// project root, and closes them with their owner. In the first run with
// write checks (2026-09-26), 4 of the 6 builds that failed to compile were of
// such files, which the workspace's servers do not see.
type scratchLanguages struct {
	start func(dir string) (*lsp.Manager, error)

	mu       sync.Mutex
	managers map[string]*lsp.Manager
	order    []string // Roots, oldest first.
	closed   bool
}

// maxScratchRoots bounds the scratch roots with a running manager per owner.
const maxScratchRoots = 4

// rootMarkers name a project's root directory.
var rootMarkers = []string{"go.mod", "go.work", "package.json", "tsconfig.json", "Cargo.toml", "pyproject.toml", "setup.py"}

func newScratchLanguages(start func(string) (*lsp.Manager, error)) *scratchLanguages {
	return &scratchLanguages{start: start, managers: map[string]*lsp.Manager{}}
}

// forPath returns the manager for path's project, starting it if needed, or
// nil when path has no root narrower than a shared directory such as /tmp.
func (p *scratchLanguages) forPath(path string) *lsp.Manager {
	root := scratchRoot(path)
	if root == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	if m, ok := p.managers[root]; ok {
		p.order = append(slices.DeleteFunc(p.order, func(r string) bool { return r == root }), root)
		return m
	}
	m, err := p.start(root)
	if err != nil {
		return nil
	}
	if len(p.order) == maxScratchRoots {
		oldest := p.order[0]
		p.order = p.order[1:]
		go func(m *lsp.Manager) { _ = m.Close(context.Background()) }(p.managers[oldest])
		delete(p.managers, oldest)
	}
	p.managers[root] = m
	p.order = append(p.order, root)
	return m
}

// Close stops every scratch manager.
func (p *scratchLanguages) Close(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	var errs []error
	for root, m := range p.managers {
		errs = append(errs, m.Close(ctx))
		delete(p.managers, root)
	}
	p.order = nil
	return errors.Join(errs...)
}

// scratchRoot is the nearest directory above path holding a root marker, else
// path's own directory; never a shared directory like /tmp or the home
// directory, or one above them, whose tree a server would have to scan.
func scratchRoot(path string) string {
	dir := filepath.Dir(path)
	for d := dir; !sharedDir(d); d = filepath.Dir(d) {
		for _, marker := range rootMarkers {
			if _, err := os.Stat(filepath.Join(d, marker)); err == nil {
				return d
			}
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	if sharedDir(dir) {
		return ""
	}
	return dir
}

// sharedDir reports whether dir is a shared directory or one above it.
func sharedDir(dir string) bool {
	home, _ := os.UserHomeDir()
	for _, shared := range []string{string(filepath.Separator), home, os.TempDir(), "/tmp", "/var/tmp"} {
		if shared == "" {
			continue
		}
		if real, err := filepath.EvalSymlinks(shared); err == nil {
			shared = real
		}
		if rel, err := filepath.Rel(dir, shared); err == nil && (rel == "." || !strings.HasPrefix(rel, "..")) {
			return true
		}
	}
	return false
}
