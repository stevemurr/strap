package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"
)

// Adapter resolves setup only. Semantic operations are implemented by the client.
// Resolve may read files, but must not start processes or modify the workspace.
// Implementations must support concurrent calls and honor cancellation.
type Adapter interface {
	Resolve(context.Context, ResolveRequest) (LaunchSpec, error)
}
type ResolveRequest struct {
	SessionDir, Path string
	Server           ServerConfig
}
type LaunchSpec struct {
	Root                  string
	Dir                   string
	Command               []string
	Env                   []string
	InitializationOptions json.RawMessage
	Settings              json.RawMessage
}
type Dependencies struct{ Adapters map[string]Adapter }

type genericAdapter struct{ goWorkspace bool }

func (a genericAdapter) Resolve(ctx context.Context, r ResolveRequest) (LaunchSpec, error) {
	if err := ctx.Err(); err != nil {
		return LaunchSpec{}, err
	}
	p := r.Path
	if p == "" {
		p = r.SessionDir
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(r.SessionDir, p)
	}
	info, err := os.Stat(p)
	if err != nil {
		return LaunchSpec{}, err
	}
	if !info.IsDir() {
		p = filepath.Dir(p)
	}
	root := ""
	for _, v := range r.Server.Roots {
		if within(v, p) && (root == "" || len(v) > len(root)) {
			root = v
		}
	}
	if len(r.Server.Roots) > 0 && root == "" {
		return LaunchSpec{}, failure("server_unavailable", "path is outside configured roots")
	}
	if root == "" && a.goWorkspace {
		work, ok := r.Server.Env["GOWORK"]
		if !ok {
			work = os.Getenv("GOWORK")
		}
		root, err = goRootWithWorkspace(ctx, p, work)
		if err != nil {
			return LaunchSpec{}, err
		}
	}
	if root == "" && !a.goWorkspace {
		for d := p; ; d = filepath.Dir(d) {
			for _, marker := range r.Server.RootMarkers {
				if _, e := os.Stat(filepath.Join(d, marker)); e == nil {
					root = d
					break
				}
			}
			if root != "" || d == r.SessionDir || filepath.Dir(d) == d || !within(r.SessionDir, d) {
				break
			}
		}
	}
	if root == "" {
		if (len(r.Server.RootMarkers) > 0 && !r.Server.FallbackToSession) || a.goWorkspace {
			return LaunchSpec{}, failure("server_unavailable", "no applicable workspace root; configure an explicit root")
		}
		root = r.SessionDir
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return LaunchSpec{}, err
	}
	env := map[string]string{}
	for _, v := range os.Environ() {
		k, v, ok := strings.Cut(v, "=")
		if ok {
			env[k] = v
		}
	}
	for k, v := range r.Server.Env {
		env[k] = v
	}
	// Pin automatic Go selection to the resolved root. A module excluded from
	// a parent go.work must not inherit that unrelated workspace.
	if a.goWorkspace && (env["GOWORK"] == "" || env["GOWORK"] == "auto") {
		if _, e := os.Stat(filepath.Join(root, "go.work")); e == nil {
			env["GOWORK"] = filepath.Join(root, "go.work")
		} else {
			env["GOWORK"] = "off"
		}
	}
	keys := slices.Sorted(maps.Keys(env))
	s := LaunchSpec{Root: root, Dir: root, Command: append([]string(nil), r.Server.Command...), InitializationOptions: append(json.RawMessage(nil), r.Server.InitializationOptions...), Settings: append(json.RawMessage(nil), r.Server.Settings...)}
	for _, k := range keys {
		s.Env = append(s.Env, k+"="+env[k])
	}
	return s, nil
}

func goRoot(ctx context.Context, p string) (string, error) {
	canonical, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	p = canonical
	module := ""
	for d := p; ; d = filepath.Dir(d) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if module == "" {
			if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
				module = d
			}
		}
		work := filepath.Join(d, "go.work")
		if b, err := readBounded(work, 1<<20); err == nil {
			f, err := modfile.ParseWork(work, b, nil)
			if err != nil {
				return "", fmt.Errorf("parse go.work: %w", err)
			}
			if p == d {
				return d, nil
			}
			for _, use := range f.Use {
				u := use.Path
				if !filepath.IsAbs(u) {
					u = filepath.Join(d, u)
				}
				u, e := filepath.EvalSymlinks(u)
				if e == nil && module == u {
					return d, nil
				}
			}
			// The nearest go.work controls Go's workspace selection.
			break
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	return module, nil
}

func within(root, path string) bool {
	r, err := filepath.Rel(root, path)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}

func goRootWithWorkspace(ctx context.Context, p, work string) (string, error) {
	if work == "" || work == "auto" {
		return goRoot(ctx, p)
	}
	p, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	module := ""
	for d := p; ; d = filepath.Dir(d) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			module = d
			break
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	if work == "off" {
		return module, nil
	}
	if !filepath.IsAbs(work) {
		return "", failure("invalid_configuration", "GOWORK must be off, auto, or an absolute path")
	}
	work, err = filepath.EvalSymlinks(work)
	if err != nil {
		return "", err
	}
	raw, err := readBounded(work, 1<<20)
	if err != nil {
		return "", err
	}
	f, err := modfile.ParseWork(work, raw, nil)
	if err != nil {
		return "", err
	}
	root := filepath.Dir(work)
	if p == root {
		return root, nil
	}
	for _, use := range f.Use {
		u := use.Path
		if !filepath.IsAbs(u) {
			u = filepath.Join(root, u)
		}
		u, err = filepath.EvalSymlinks(u)
		if err == nil && u == module {
			return root, nil
		}
	}
	return "", failure("server_unavailable", "configured GOWORK does not include this module")
}
