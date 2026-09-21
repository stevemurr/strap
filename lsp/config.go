// Package lsp provides session-owned language servers and normalized code queries.
package lsp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Config is host policy. Dir is supplied by the harness, not by model arguments.
type Config struct {
	Dir                 string         `json:"-"`
	Servers             []ServerConfig `json:"servers"`
	StartTimeoutMS      int            `json:"start_timeout_ms,omitempty"`
	RequestTimeoutMS    int            `json:"request_timeout_ms,omitempty"`
	DiagnosticTimeoutMS int            `json:"diagnostic_timeout_ms,omitempty"`
	MaxServers          int            `json:"max_servers,omitempty"`
	MaxFileBytes        int            `json:"max_file_bytes,omitempty"`
	MaxMessageBytes     int            `json:"max_message_bytes,omitempty"`
	MaxWorkspaceFiles   int            `json:"max_workspace_files,omitempty"`
	MaxWorkspaceBytes   int            `json:"max_workspace_bytes,omitempty"`
	MaxOpenDocuments    int            `json:"max_open_documents,omitempty"`
	CacheBytes          int            `json:"cache_bytes,omitempty"`
}

type ServerConfig struct {
	ID                    string            `json:"id"`
	Adapter               string            `json:"adapter,omitempty"`
	Command               []string          `json:"command"`
	Languages             map[string]string `json:"languages"`       // File suffix -> LSP language ID.
	Roots                 []string          `json:"roots,omitempty"` // Relative to Config.Dir.
	RootMarkers           []string          `json:"root_markers,omitempty"`
	FallbackToSession     bool              `json:"fallback_to_session,omitempty"` // Use Dir when no root marker is found.
	Env                   map[string]string `json:"env,omitempty"`
	InitializationOptions json.RawMessage   `json:"initialization_options,omitempty"`
	Settings              json.RawMessage   `json:"settings,omitempty"`
}

func GoConfig() Config {
	return Config{Servers: []ServerConfig{{ID: "gopls", Adapter: "gopls", Command: []string{"gopls"}, Languages: map[string]string{".go": "go"}}}}
}

// Load rejects unknown host fields, while server-owned JSON remains opaque.
func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return Config{}, err
	}
	if len(b) > 1<<20 {
		return Config{}, fmt.Errorf("LSP configuration exceeds 1 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	var c Config
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return c, fmt.Errorf("LSP configuration must contain one JSON object")
	}
	return c, nil
}

func (c Config) Clone() Config {
	c.Servers = slices.Clone(c.Servers)
	for i := range c.Servers {
		s := &c.Servers[i]
		s.Command = slices.Clone(s.Command)
		s.Languages = maps.Clone(s.Languages)
		s.Roots = slices.Clone(s.Roots)
		s.RootMarkers = slices.Clone(s.RootMarkers)
		s.Env = maps.Clone(s.Env)
		s.InitializationOptions = bytes.Clone(s.InitializationOptions)
		s.Settings = bytes.Clone(s.Settings)
	}
	return c
}

func (c *Config) defaults() error {
	if c.Dir == "" {
		c.Dir = "."
	}
	p, err := filepath.Abs(c.Dir)
	if err != nil {
		return err
	}
	c.Dir, err = filepath.EvalSymlinks(p)
	if err != nil {
		return err
	}
	info, err := os.Stat(c.Dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("LSP directory must be a directory")
	}
	for _, v := range []struct {
		p   *int
		def int
	}{
		{&c.StartTimeoutMS, 30000}, {&c.RequestTimeoutMS, 15000}, {&c.DiagnosticTimeoutMS, 1500},
		{&c.MaxServers, 8}, {&c.MaxFileBytes, 1 << 20}, {&c.MaxMessageBytes, 8 << 20},
		{&c.MaxWorkspaceFiles, 10000}, {&c.MaxWorkspaceBytes, 32 << 20}, {&c.MaxOpenDocuments, 128}, {&c.CacheBytes, 8 << 20},
	} {
		if *v.p == 0 {
			*v.p = v.def
		}
		if *v.p < 1 {
			return fmt.Errorf("LSP limits must be positive")
		}
	}
	if c.StartTimeoutMS > 300000 || c.RequestTimeoutMS > 300000 || c.DiagnosticTimeoutMS > 30000 || c.MaxFileBytes > c.MaxMessageBytes/2 || c.CacheBytes < 32768 {
		return fmt.Errorf("invalid LSP timeout or byte limits")
	}
	if len(c.Servers) == 0 || len(c.Servers) > 32 {
		return fmt.Errorf("configure 1..32 language servers")
	}
	ids := map[string]bool{}
	for i := range c.Servers {
		s := &c.Servers[i]
		if strings.TrimSpace(s.ID) == "" || ids[s.ID] {
			return fmt.Errorf("LSP server IDs must be nonempty and unique")
		}
		ids[s.ID] = true
		if s.Adapter == "" {
			s.Adapter = "generic"
		}
		if len(s.Command) == 0 || s.Command[0] == "" || len(s.Languages) == 0 {
			return fmt.Errorf("server %s needs command and languages", s.ID)
		}
		for suffix, id := range s.Languages {
			if !strings.HasPrefix(suffix, ".") || strings.ContainsAny(suffix, "/\\") || id == "" {
				return fmt.Errorf("invalid language mapping for %s", s.ID)
			}
		}
		for k, v := range s.Env {
			if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
				return fmt.Errorf("invalid environment for %s", s.ID)
			}
		}
		for _, v := range []json.RawMessage{s.InitializationOptions, s.Settings} {
			if len(v) > 0 && !json.Valid(v) {
				return fmt.Errorf("invalid server JSON for %s", s.ID)
			}
		}
		for j, root := range s.Roots {
			if !filepath.IsAbs(root) {
				root = filepath.Join(c.Dir, root)
			}
			root, err = filepath.EvalSymlinks(root)
			if err != nil {
				return err
			}
			info, err = os.Stat(root)
			if err != nil {
				return err
			}
			if !info.IsDir() {
				return fmt.Errorf("LSP root must be a directory")
			}
			s.Roots[j] = root
		}
		for _, marker := range s.RootMarkers {
			if marker == "" || filepath.Base(marker) != marker {
				return fmt.Errorf("root markers must be simple file names")
			}
		}
	}
	return nil
}

func milliseconds(n int) time.Duration { return time.Duration(n) * time.Millisecond }
