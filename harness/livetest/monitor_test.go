// Package livetest drives full sessions against a live model endpoint and
// checks the user → manager → worker behaviour through recorded transitions.
//
//	STRAP_LIVE_MANAGER=1 go test ./harness/livetest -timeout 90m -v
//
// STRAP_LIVE_PROFILE selects a model profile (default: the catalog default),
// STRAP_LIVE_MODELS a models.json path, and STRAP_LIVE_OUTPUT the directory
// that keeps each scenario's trace.jsonl and transitions.jsonl.
package livetest_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/machine"
)

// monitor feeds a live session's events to a machine.Recorder, writes every
// transition to transitions.jsonl as it happens, and lets scenarios wait on
// the recorded state. Hold mu to read the recorder.
type monitor struct {
	*machine.Recorder
	t     *testing.T
	start time.Time
	out   io.Writer

	mu         sync.Mutex
	wake       chan struct{}
	err        error
	lastChange time.Time
}

func newMonitor(t *testing.T, s *harness.Session, path string) *monitor {
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	m := &monitor{Recorder: machine.NewRecorder(s.Manager()), t: t, start: time.Now(), out: out, wake: make(chan struct{}), lastChange: time.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			e, err := s.NextEvent(ctx)
			m.mu.Lock()
			if err != nil {
				if ctx.Err() == nil {
					m.err = err
				}
				m.notifyLocked()
				m.mu.Unlock()
				return
			}
			for _, tr := range m.Observe(m.since(), e) {
				m.logLocked(tr)
			}
			m.notifyLocked()
			m.mu.Unlock()
		}
	}()
	t.Cleanup(func() { cancel(); <-done; out.Close() })
	return m
}

func (m *monitor) since() time.Duration { return time.Since(m.start) }

func (m *monitor) notifyLocked() {
	m.lastChange = time.Now()
	close(m.wake)
	m.wake = make(chan struct{})
}

func (m *monitor) logLocked(tr machine.Transition) {
	line, _ := json.Marshal(tr)
	fmt.Fprintf(m.out, "%s\n", line)
	m.t.Logf("%7.2fs %-12s %-9s %s", tr.At, tr.Kind, tr.Agent, tr.Detail)
}

// await blocks until cond holds, the session's event stream ends, or timeout.
func (m *monitor) await(what string, timeout time.Duration, cond func() bool) {
	m.t.Helper()
	deadline := time.After(timeout)
	for {
		m.mu.Lock()
		ok, err, wake := cond(), m.err, m.wake
		m.mu.Unlock()
		if ok {
			return
		}
		if err != nil {
			m.t.Fatalf("waiting for %s: session events ended: %v", what, err)
		}
		select {
		case <-wake:
		case <-time.After(time.Second): // Re-check time-based conditions.
		case <-deadline:
			m.t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
	}
}

// awaitSettled waits until the session has stayed settled for quiet after
// the manager replied to the latest user message. A session already settled when
// that message was sent must not count.
func (m *monitor) awaitSettled(timeout, quiet time.Duration) {
	m.t.Helper()
	m.await("the session to settle", timeout, func() bool {
		after := time.Duration(0)
		if n := len(m.Turns); n > 0 {
			after = m.Turns[n-1].At
		}
		return m.Settled() && len(m.RepliesToUser(after)) > 0 && time.Since(m.lastChange) >= quiet
	})
	m.mu.Lock()
	m.logLocked(machine.Transition{At: m.since().Round(10 * time.Millisecond).Seconds(), Kind: "settled", Detail: "manager answered, nothing owed, nothing running"})
	m.mu.Unlock()
}
