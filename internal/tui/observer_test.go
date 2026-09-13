package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/message"
)

// subscribing is a session that can be observed through the event log.
type subscribing struct {
	*fakeSession
	err       error
	tokens    int64
	tokensErr error
	automatic bool
}

func (s *subscribing) ID() string { return "session" }
func (s *subscribing) Subscribe(context.Context, harness.SubscribeOptions) (*eventlog.Subscription, error) {
	return nil, s.err
}
func (s *subscribing) CountAgentTokens(context.Context, message.ActorID, uint64) (int64, error) {
	return s.tokens, s.tokensErr
}
func (s *subscribing) AutomaticContextTokens() bool { return s.automatic }

// A session that cannot be subscribed to is wrapped so the UI still starts and
// reports the subscription failure on its first read, rather than crashing.
func TestObserveSessionSurfacesSubscriptionFailure(t *testing.T) {
	sentinel := errors.New("log unavailable")
	observed, detach := observeSession(&subscribing{fakeSession: &fakeSession{}, err: sentinel})
	defer detach()
	if _, err := observed.NextEvent(context.Background()); !errors.Is(err, sentinel) {
		t.Fatal("the subscription failure was not reported to the UI", err)
	}
	// The rest of the session is still reachable through the wrapper.
	if observed.Root() != "root" {
		t.Fatal(observed.Root())
	}
}

// A session with no event log is used directly, with no observation wrapper.
func TestObserveSessionPassesThroughAPlainSession(t *testing.T) {
	session := &fakeSession{}
	observed, detach := observeSession(session)
	defer detach()
	if observed != Session(session) {
		t.Fatal("a session with no log was wrapped anyway")
	}
}

// On-demand token counting stays available through the observation wrapper, and
// reports plainly when the underlying session cannot measure.
func TestObservedSessionForwardsTokenCapabilities(t *testing.T) {
	inner := &subscribing{fakeSession: &fakeSession{}, err: errors.New("no log"), tokens: 17, automatic: true}
	// failedSession also wraps the session, so exercise the wrapper directly.
	wrapper := &observedSession{Session: inner}
	n, err := wrapper.CountAgentTokens(context.Background(), "root", 1)
	if err != nil || n != 17 {
		t.Fatal(n, err)
	}
	if !wrapper.AutomaticContextTokens() {
		t.Fatal("automatic measurement was not forwarded")
	}
	// A session without those capabilities reports their absence rather than zero.
	bare := &observedSession{Session: &fakeSession{}}
	if _, err := bare.CountAgentTokens(context.Background(), "root", 1); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatal(err)
	}
	if bare.AutomaticContextTokens() {
		t.Fatal("a session that cannot measure reported automatic measurement")
	}
}
