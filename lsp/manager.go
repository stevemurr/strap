package lsp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

type instance struct {
	config      ServerConfig
	spec        LaunchSpec
	key         string
	generation  uint64
	client      *client
	nextVersion int
	documents   map[string]*document
	files       map[string]string
	scanned     bool
	partial     bool
	issues      []string
	lastError   string
	failures    int
}
type document struct {
	text, hash, language string
	version              int
	used                 uint64
	changedAt            time.Time
}
type reference struct {
	path, hash, key string
	generation      uint64
	position        Position
	declaration     *Range
}
type retainedPage struct {
	key       string
	items     []Item
	metadata  Metadata
	epoch     uint64
	truncated bool
	created   time.Time
	bytes     int
}

// Manager serializes query/synchronization transactions. Protocol callbacks use
// separate client locks, so a server can ask for configuration during a query.
// The manager is shared by agents, but never by independently edited worktrees.
type Manager struct {
	config    Config
	adapters  map[string]Adapter
	gate      chan struct{}
	life      context.Context
	cancel    context.CancelFunc
	epoch     atomic.Uint64
	instances map[string]*instance
	refs      map[string]reference
	refOrder  []string
	pages     map[string]*retainedPage
	pageBytes int
	sequence  uint64
	prefix    string
	watcher   *fsnotify.Watcher
	watched   map[string]bool
	watchGap  atomic.Bool
	wg        sync.WaitGroup
	changes   chan string
	closed    bool
}

func New(config Config, deps Dependencies) (*Manager, error) {
	config = config.Clone()
	if err := config.defaults(); err != nil {
		return nil, err
	}
	adapters := map[string]Adapter{"generic": genericAdapter{}, "gopls": genericAdapter{goWorkspace: true}}
	for id, a := range deps.Adapters {
		if id == "" || a == nil || adapters[id] != nil {
			return nil, fmt.Errorf("invalid or duplicate LSP adapter %q", id)
		}
		adapters[id] = a
	}
	for _, s := range config.Servers {
		if adapters[s.Adapter] == nil {
			return nil, fmt.Errorf("unknown LSP adapter %q", s.Adapter)
		}
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(context.Background())
	var id [8]byte
	if _, err = rand.Read(id[:]); err != nil {
		w.Close()
		cancel()
		return nil, err
	}
	m := &Manager{config: config, adapters: adapters, gate: make(chan struct{}, 1), life: life, cancel: cancel, instances: map[string]*instance{}, refs: map[string]reference{}, pages: map[string]*retainedPage{}, prefix: hex.EncodeToString(id[:]), watcher: w, watched: map[string]bool{}, changes: make(chan string, 128)}
	m.wg.Add(2)
	go m.watch()
	go m.refresh()
	return m, nil
}
func (m *Manager) Config() Config { return m.config.Clone() }
func (m *Manager) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case m.gate <- struct{}{}:
	}
	if err := ctx.Err(); err != nil {
		m.release()
		return err
	}
	if m.closed {
		m.release()
		return failure("closed", "language services closed")
	}
	return nil
}
func (m *Manager) release() { <-m.gate }
func (m *Manager) requestContext(ctx context.Context) (context.Context, func()) {
	c, cancel := context.WithTimeout(ctx, milliseconds(m.config.StartTimeoutMS+m.config.RequestTimeoutMS+m.config.DiagnosticTimeoutMS))
	stop := context.AfterFunc(m.life, cancel)
	return c, func() { stop(); cancel() }
}
func (m *Manager) id(kind string) string {
	m.sequence++
	return fmt.Sprintf("%s_%s_%d", kind, m.prefix, m.sequence)
}
func (m *Manager) path(path string) (string, error) {
	if path == "" {
		return "", failure("invalid_path", "path must not be empty")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(m.config.Dir, path)
	}
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Abs(p)
}
func (m *Manager) display(path string) string {
	if within(m.config.Dir, path) {
		p, _ := filepath.Rel(m.config.Dir, path)
		return filepath.ToSlash(p)
	}
	return path
}
func language(s ServerConfig, path string) string {
	best, id := "", ""
	for suffix, v := range s.Languages {
		if strings.HasSuffix(path, suffix) && len(suffix) > len(best) {
			best, id = suffix, v
		}
	}
	return id
}

func (m *Manager) resolve(ctx context.Context, s ServerConfig, path string) (*instance, error) {
	// Adapters receive owned input, and the manager keeps an owned result.
	sc := Config{Servers: []ServerConfig{s}}.Clone().Servers[0]
	spec, err := m.adapters[s.Adapter].Resolve(ctx, ResolveRequest{SessionDir: m.config.Dir, Path: path, Server: sc})
	if err != nil {
		return nil, err
	}
	if spec.Root == "" || spec.Dir == "" || len(spec.Command) == 0 || spec.Command[0] == "" {
		return nil, failure("invalid_configuration", "adapter returned an incomplete launch specification")
	}
	spec.Root, err = filepath.EvalSymlinks(spec.Root)
	if err != nil {
		return nil, err
	}
	spec.Root, err = filepath.Abs(spec.Root)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(struct {
		ID   string
		Spec LaunchSpec
	}{s.ID, spec})
	if err != nil {
		return nil, err
	}
	var copied struct {
		ID   string
		Spec LaunchSpec
	}
	_ = json.Unmarshal(b, &copied)
	spec = copied.Spec
	key := digest(b)
	if v := m.instances[key]; v != nil {
		return v, nil
	}
	if len(m.instances) >= m.config.MaxServers {
		return nil, failure("resource_limit", "maximum language server instances reached; narrow configured roots")
	}
	v := &instance{config: s, spec: spec, key: key, documents: map[string]*document{}, files: map[string]string{}}
	m.instances[key] = v
	return v, nil
}
func (m *Manager) forFile(ctx context.Context, path string) (*instance, string, error) {
	var candidates []ServerConfig
	for _, s := range m.config.Servers {
		if language(s, path) == "" {
			continue
		}
		if len(s.Roots) > 0 {
			ok := false
			for _, root := range s.Roots {
				ok = ok || within(root, path)
			}
			if !ok {
				continue
			}
		}
		candidates = append(candidates, s)
	}
	if len(candidates) == 0 {
		return nil, "", failure("server_unavailable", "no configured language server for %s", m.display(path))
	}
	if len(candidates) > 1 {
		return nil, "", failure("ambiguous_root", "multiple language servers match %s; use non-overlapping roots or file mappings", m.display(path))
	}
	s, err := m.resolve(ctx, candidates[0], path)
	return s, language(candidates[0], path), err
}
func (m *Manager) ensure(ctx context.Context, s *instance) error {
	if s.client != nil && s.client.alive() {
		return nil
	}
	if s.client != nil {
		s.client.abort()
		s.client = nil
		s.documents = map[string]*document{}
		s.scanned = false
		s.files = map[string]string{}
		s.failures++
	}
	if s.failures >= 2 {
		return failure("server_unavailable", "%s repeatedly failed; restart the session after correcting its setup", s.config.ID)
	}
	start, cancel := context.WithTimeout(ctx, milliseconds(m.config.StartTimeoutMS))
	defer cancel()
	c, err := startClient(start, s.spec, m.config, m.changes)
	if err != nil {
		s.lastError = cut(err.Error(), 1024)
		s.failures++
		return err
	}
	s.client = c
	s.generation++
	s.lastError = ""
	return nil
}

// Changed is a nonblocking hint; file hooks must call it after releasing their
// mutation lock. Empty path means a shell/external writer may have changed anything.
func (m *Manager) Changed(path string) {
	path = canonicalChanged(m.config.Dir, path)
	m.epoch.Add(1)
	select {
	case m.changes <- path:
	default:
		m.watchGap.Store(true)
	}
}
func (m *Manager) watch() {
	defer m.wg.Done()
	for {
		select {
		case <-m.life.Done():
			return
		case event, ok := <-m.watcher.Events:
			if !ok {
				return
			}
			m.Changed(event.Name)
		case _, ok := <-m.watcher.Errors:
			if !ok {
				return
			}
			m.watchGap.Store(true)
			m.epoch.Add(1)
		}
	}
}

func (m *Manager) Close(ctx context.Context) error {
	m.cancel() // Cancel active/queued transactions before waiting for the gate.
	select {
	case m.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer m.release()
	m.closed = true
	_ = m.watcher.Close()
	m.wg.Wait()
	var errs []error
	for _, s := range m.instances {
		if s.client != nil {
			errs = append(errs, s.client.close(ctx))
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) Status(ctx context.Context, path string) (Status, error) {
	if err := m.acquire(ctx); err != nil {
		return Status{}, err
	}
	defer m.release()
	out := Status{Servers: []ServerStatus{}}
	filter := ""
	if path != "" {
		p, err := m.path(path)
		if err != nil {
			return out, err
		}
		filter = p
	}
	for _, cfg := range m.config.Servers {
		found := false
		for _, s := range m.instances {
			if s.config.ID != cfg.ID || (filter != "" && !within(s.spec.Root, filter)) {
				continue
			}
			found = true
			v := ServerStatus{ID: cfg.ID, Root: s.spec.Root, State: "configured", Generation: s.generation, Error: s.lastError}
			if s.client != nil {
				v.Version = s.client.version
				v.State = "stopped"
				if s.client.alive() {
					v.State = "running"
				}
				for name, cap := range operationCapabilities {
					if s.client.supported(cap) {
						v.Operations = append(v.Operations, name)
					}
				}
				sort.Strings(v.Operations)
			}
			out.Servers = append(out.Servers, v)
		}
		if !found {
			v := ServerStatus{ID: cfg.ID, State: "configured"}
			if filter != "" {
				spec, e := m.adapters[cfg.Adapter].Resolve(ctx, ResolveRequest{SessionDir: m.config.Dir, Path: filter, Server: cfg})
				if e != nil {
					v.Error = cut(e.Error(), 1024)
				} else {
					v.Root = spec.Root
				}
			}
			out.Servers = append(out.Servers, v)
		}
	}
	sort.Slice(out.Servers, func(i, j int) bool {
		a, b := out.Servers[i], out.Servers[j]
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Root < b.Root
	})
	return out, nil
}

var operationCapabilities = map[string]string{"symbols": "workspaceSymbolProvider", "outline": "documentSymbolProvider", "inspect": "hoverProvider", "definition": "definitionProvider", "declaration": "declarationProvider", "type_definition": "typeDefinitionProvider", "implementation": "implementationProvider", "references": "referencesProvider", "pull_diagnostics": "diagnosticProvider"}

func (m *Manager) metadata(s *instance) Metadata {
	return Metadata{Sources: []Source{{s.config.ID, s.spec.Root, s.generation, s.key}}, Freshness: "synchronized", Coverage: "server-selected build; observed workspace files only; dependencies outside the root and excluded directories are not watched", Partial: s.partial, Issues: append([]string(nil), s.issues...)}
}
func (m *Manager) hasChanged(epoch uint64, meta *Metadata) {
	if m.epoch.Load() != epoch {
		meta.Freshness = "stale"
		meta.Issues = append(meta.Issues, "workspace changed while the result was being collected")
	}
}
