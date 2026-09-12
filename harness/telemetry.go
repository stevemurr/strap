package harness

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/identity"
)

// TelemetryConfig governs automatic I/O independently of observer attachment.
// Explicit CountAgentTokens remains a separate, caller-requested operation.
type TelemetryConfig struct {
	ContextTokens bool
	Concurrency   int
	Queue         int
	Timeout       time.Duration
}

func (c *TelemetryConfig) defaults() error {
	if c.Concurrency == 0 {
		c.Concurrency = 2
	}
	if c.Queue == 0 {
		c.Queue = 128
	}
	if c.Timeout == 0 {
		c.Timeout = 10 * time.Second
	}
	if c.Concurrency < 1 || c.Concurrency > 64 || c.Queue < 1 || c.Timeout <= 0 {
		return errors.New("invalid telemetry concurrency, queue, or timeout")
	}
	return nil
}

type telemetry struct {
	session *Session
	mu      sync.Mutex
	stopped bool
	latest  map[identity.ActorID]uint64
	tasks   chan conversation.ToolBatchEvent
	wg      sync.WaitGroup
}

func newTelemetry(s *Session) *telemetry {
	t := &telemetry{session: s, latest: make(map[identity.ActorID]uint64), tasks: make(chan conversation.ToolBatchEvent, s.config.Telemetry.Queue)}
	if s.config.Telemetry.ContextTokens {
		for i := 0; i < s.config.Telemetry.Concurrency; i++ {
			t.wg.Add(1)
			go t.run()
		}
	}
	return t
}
func (t *telemetry) schedule(e conversation.ToolBatchEvent) {
	if !t.session.config.Telemetry.ContextTokens || e.Batch.ContextRevision == 0 || len(e.Batch.Calls) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped || e.Batch.ContextRevision <= t.latest[e.Agent] {
		return
	}
	t.latest[e.Agent] = e.Batch.ContextRevision
	select {
	case t.tasks <- e:
	default:
		t.session.publish(conversation.ContextTokensEvent{Agent: e.Agent, Revision: e.Batch.ContextRevision, Error: "automatic token count queue full"})
	}
}
func (t *telemetry) stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.stopped {
		t.stopped = true
		close(t.tasks)
	}
}
func (t *telemetry) run() {
	defer t.wg.Done()
	for e := range t.tasks {
		ctx, cancel := context.WithTimeout(context.Background(), t.session.config.Telemetry.Timeout)
		n, err := t.session.CountAgentTokens(ctx, e.Agent, e.Batch.ContextRevision)
		cancel()
		v := conversation.ContextTokensEvent{Agent: e.Agent, Revision: e.Batch.ContextRevision, Count: n}
		if err != nil {
			v.Error = err.Error()
		} else if n < 0 {
			v.Error = "negative token count"
		}
		t.session.publish(v)
	}
}

func (s *Session) AutomaticContextTokens() bool { return s.config.Telemetry.ContextTokens }
