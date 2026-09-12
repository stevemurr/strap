package harness_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/inbox"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/work"
)

type blockedProvider struct {
	started, cancelled, release chan struct{}
}

func (p *blockedProvider) Submit(ctx context.Context, _ provider.Request, observer provider.Observer) (provider.Response, error) {
	close(p.started)
	<-ctx.Done()
	close(p.cancelled)
	<-p.release // Simulate a dependency still unwinding after cancellation.
	return provider.Response{}, ctx.Err()
}

func newLifecycleSession(t *testing.T, ctx context.Context, p provider.Provider, resources ...harness.OwnedResource) *harness.Session {
	t.Helper()
	cfg := harness.DefaultConfig()
	cfg.LocalTools, cfg.Web = false, nil
	s, err := harness.New(ctx, cfg, harness.Dependencies{Provider: p, Resources: resources})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return s
}

func TestCloseTimeoutKeepsOwnershipAndDrainsTail(t *testing.T) {
	p := &blockedProvider{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	owned := &closer{}
	s := newLifecycleSession(t, context.Background(), p, harness.OwnedResource{Name: "test", Resource: owned})
	if _, err := s.Send(s.Root(), "run"); err != nil {
		t.Fatal(err)
	}
	<-p.started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	<-p.cancelled
	if s.State() != harness.Closing || owned.calls.Load() != 0 {
		t.Fatal("premature cleanup", s.State(), owned.calls.Load())
	}
	if _, err := s.Send(s.Root(), "late"); !errors.Is(err, harness.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := s.AssignWork(context.Background(), s.Root(), work.AssignmentRequest{Kind: work.Implementation, Task: "late"}); !errors.Is(err, harness.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := s.CreateAgent(s.Root(), agent.Spec{Provider: idle{}}); !errors.Is(err, harness.ErrClosed) {
		t.Fatal(err)
	}
	close(p.release)
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.State() != harness.Closed || owned.calls.Load() != 1 {
		t.Fatal(s.State(), owned.calls.Load())
	}
	found := false
	for {
		e, err := s.NextEvent(context.Background())
		if errors.Is(err, inbox.ErrClosed) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if exit, ok := e.(conversation.AgentExited); ok && exit.Agent == s.Root() {
			found = true
		}
	}
	if !found {
		t.Fatal("final agent exit was lost")
	}
	if _, err := s.InspectAgent(s.Root(), conversation.InspectOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupFailureRetriesOnlyUnfinishedResources(t *testing.T) {
	bad, good := &closer{}, &closer{}
	bad.fail.Store(true)
	s := newLifecycleSession(t, context.Background(), idle{}, harness.OwnedResource{Name: "bad", Resource: bad}, harness.OwnedResource{Name: "good", Resource: good})
	if err := s.Close(context.Background()); err == nil {
		t.Fatal("missing cleanup error")
	}
	if s.State() != harness.Closing {
		t.Fatal(s.State())
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if bad.calls.Load() != 2 || good.calls.Load() != 1 {
		t.Fatal(bad.calls.Load(), good.calls.Load())
	}
}

type notifyCloser chan struct{}

func (c notifyCloser) Close(context.Context) error { close(c); return nil }

func TestParentCancellationClosesSessionWithoutHostWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	closed := make(notifyCloser)
	s := newLifecycleSession(t, ctx, idle{}, harness.OwnedResource{Name: "notify", Resource: closed})
	other := newLifecycleSession(t, context.Background(), idle{})
	cancel()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("owner cancellation leaked resources")
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Send(other.Root(), "still running"); err != nil {
		t.Fatal(err)
	}
}

type blockedCounter struct {
	idle
	started, cancelled, release chan struct{}
}

func (p *blockedCounter) CountTokens(ctx context.Context, _ provider.Request) (int64, error) {
	close(p.started)
	<-ctx.Done()
	close(p.cancelled)
	<-p.release
	return 0, ctx.Err()
}

func TestShutdownCancelsAndJoinsHostTokenCount(t *testing.T) {
	p := &blockedCounter{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	owned := &closer{}
	s := newLifecycleSession(t, context.Background(), p, harness.OwnedResource{Name: "test", Resource: owned})
	info, err := s.InspectAgent(s.Root(), conversation.InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	counted := make(chan error, 1)
	go func() {
		_, err := s.CountAgentTokens(context.Background(), s.Root(), info.ContextRevision)
		counted <- err
	}()
	select {
	case <-p.started:
	case err := <-counted:
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	<-p.cancelled
	if owned.calls.Load() != 0 {
		t.Fatal("closed resources during count")
	}
	close(p.release)
	if err := <-counted; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type failingProvider struct{}

func (failingProvider) Submit(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
	return provider.Response{}, errors.New("model execution failed")
}
func TestTerminalOutcomeSurvivesStorageDisposal(t *testing.T) {
	s := newLifecycleSession(t, context.Background(), failingProvider{})
	if _, err := s.Send(s.Root(), "run"); err != nil {
		t.Fatal(err)
	}
	for {
		e, err := s.NextEvent(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := e.(conversation.AgentExited); ok {
			break
		}
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := s.Inspect()
	if before.Outcome == nil || before.Outcome.Error != "model execution failed" || before.Outcome.Reason != "requested" || before.Outcome.CleanupAttempts != 1 {
		t.Fatal(before)
	}
	if err := s.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := s.Inspect()
	if after.State != harness.Disposed || after.Outcome.Error != before.Outcome.Error || !after.Capture.Disposed {
		t.Fatal(after)
	}
}
func TestCleanupFailureIsInspectableBeforeRetryAndRecorded(t *testing.T) {
	owned := &closer{}
	owned.fail.Store(true)
	s := newLifecycleSession(t, context.Background(), idle{}, harness.OwnedResource{Name: "test", Resource: owned})
	if err := s.Close(context.Background()); err == nil {
		t.Fatal("expected cleanup failure")
	}
	if info := s.Inspect(); info.Outcome == nil || info.Outcome.CleanupError == "" {
		t.Fatal(info)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	info := s.Inspect()
	if info.Outcome.CleanupAttempts != 2 || info.Outcome.CleanupError != "" {
		t.Fatal(info)
	}
	found := false
	for {
		e, err := s.NextEvent(context.Background())
		if errors.Is(err, inbox.ErrClosed) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if d, ok := e.(conversation.DiagnosticEvent); ok && d.Message == "Session cleanup failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("cleanup diagnostic lost")
	}
}
