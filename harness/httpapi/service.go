// Package httpapi adapts the Go harness API to HTTP without owning execution policy.
package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/internal/admission"
	"github.com/stevemurr/strap/internal/resource"
)

type Capability string

const (
	Read    Capability = "read"
	Command Capability = "command"
	Measure Capability = "measure"
	Create  Capability = "create"
	Dispose Capability = "dispose"
)

var ErrUnauthorized = errors.New("HTTP access denied")
var ErrNotFound = errors.New("session not found")

type Options struct {
	MaxRequests   int // Concurrent HTTP requests, including open streams; zero defaults to 256.
	DefaultConfig harness.Config
	// Authorize is required. A command capability grants trusted host operations,
	// including selecting work actors; it is not model-tool authority.
	Authorize func(*http.Request, Capability, string) error
	// Factory receives the service lifetime, never a request/disconnect context.
	Factory func(context.Context, harness.Config) (*harness.Session, error)
	// Agent profiles resolve executable collaborators on the host. They are not
	// serialized in HTTP requests. Nil disables dynamic agent creation over HTTP.
	AgentProfile func(*harness.Session, string) (agent.Spec, error)
}

// BearerToken authorizes all host capabilities for one configured token. Serve
// non-loopback traffic over TLS. Empty tokens never authorize a request.
func BearerToken(token string) func(*http.Request, Capability, string) error {
	expected := sha256.Sum256([]byte(token))
	return func(r *http.Request, _ Capability, _ string) error {
		auth := r.Header.Get("Authorization")
		actual := sha256.Sum256([]byte(strings.TrimPrefix(auth, "Bearer ")))
		if token == "" || !strings.HasPrefix(auth, "Bearer ") || subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
			return ErrUnauthorized
		}
		return nil
	}
}

// Service owns its registered sessions. Closing an HTTP response never closes a
// session. Close cancels service admission and disposes owned sessions; a caller
// timeout retains ownership so another Close can finish cleanup.
type Service struct {
	requests  chan struct{}
	cleanup   *resource.Group
	ctx       context.Context
	cancel    context.CancelFunc
	options   Options
	mu        sync.Mutex
	sessions  map[string]*harness.Session
	gate      *admission.Gate
	stopOwner func() bool
}

func New(ctx context.Context, options Options) (*Service, error) {
	if options.MaxRequests == 0 {
		options.MaxRequests = 256
	}
	if options.MaxRequests < 1 {
		return nil, errors.New("MaxRequests must be positive")
	}
	if options.Authorize == nil {
		return nil, errors.New("HTTP authorization callback is required")
	}
	if options.Factory == nil {
		options.Factory = func(ctx context.Context, c harness.Config) (*harness.Session, error) {
			return harness.New(ctx, c, harness.Dependencies{})
		}
	}
	options.DefaultConfig = options.DefaultConfig.Clone()
	run, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s := &Service{requests: make(chan struct{}, options.MaxRequests), cleanup: resource.New(), ctx: run, cancel: cancel, options: options, sessions: make(map[string]*harness.Session), gate: admission.New(run)}
	s.mu.Lock()
	s.stopOwner = context.AfterFunc(ctx, func() { _ = s.Close(context.Background()) })
	s.mu.Unlock()
	return s, nil
}
func (s *Service) create(ctx context.Context, c harness.Config) (*harness.Session, error) {
	_, done, err := s.gate.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	session, err := s.options.Factory(s.ctx, c.Clone())
	if err != nil {
		var failed *harness.StartupError
		if errors.As(err, &failed) {
			s.mu.Lock()
			s.cleanup.Add("failed session startup", failed)
			s.mu.Unlock()
		}
		return nil, err
	}
	if session == nil {
		return nil, errors.New("session factory returned nil")
	}
	s.mu.Lock()
	s.sessions[session.ID()] = session
	s.mu.Unlock()
	return session, nil
}
func (s *Service) lookup(id string) (*harness.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.sessions[id]
	if v == nil {
		return nil, ErrNotFound
	}
	return v, nil
}
func (s *Service) ids() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
func (s *Service) Close(ctx context.Context) error {
	s.gate.Seal()
	s.cancel()
	if err := s.gate.Wait(ctx); err != nil {
		return err
	}
	var errs []error
	for _, id := range s.ids() {
		session, _ := s.lookup(id)
		if err := session.Dispose(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if err := s.cleanup.Close(ctx); err != nil {
		errs = append(errs, err)
	}
	if len(errs) == 0 {
		s.mu.Lock()
		if s.stopOwner != nil {
			s.stopOwner()
		}
		s.mu.Unlock()
	}
	return errors.Join(errs...)
}
