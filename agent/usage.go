package agent

import (
	"sync"

	"github.com/stevemurr/strap/provider"
)

// UsageObservation describes a returned Submit call. ContextRevision identifies
// the history snapshot sent to the provider, before appending its response.
// A newer history revision needs a new measurement; usage is not context size.
type UsageObservation struct {
	Call            uint64          `json:"call"`
	ContextRevision uint64          `json:"context_revision"`
	Usage           *provider.Usage `json:"usage,omitempty"`
}

// UsageSnapshot sums reported counts across returned calls, including failures.
// Missing counts make the corresponding total incomplete. Latest is nil before
// any call returns; a call with no usage still replaces the latest observation.
type UsageSnapshot struct {
	Calls              uint64            `json:"calls"`
	InputTokens        int64             `json:"input_tokens"`
	OutputTokens       int64             `json:"output_tokens"`
	MissingInputCalls  uint64            `json:"missing_input_calls"`
	MissingOutputCalls uint64            `json:"missing_output_calls"`
	Latest             *UsageObservation `json:"latest,omitempty"`
}

type usageTracker struct {
	mu       sync.RWMutex
	snapshot UsageSnapshot
}

// Usage returns independent accounting without waiting for model/tool execution.
// It remains available after the agent stops and does not consume host events.
func (a *Agent) Usage() UsageSnapshot {
	a.usage.mu.RLock()
	defer a.usage.mu.RUnlock()
	snapshot := a.usage.snapshot
	if snapshot.Latest != nil {
		latest := *snapshot.Latest
		latest.Usage = latest.Usage.Clone()
		snapshot.Latest = &latest
	}
	return snapshot
}

func (a *Agent) recordUsage(revision uint64, reported *provider.Usage) error {
	a.usage.mu.Lock()
	s := &a.usage.snapshot
	s.Calls++
	observation := UsageObservation{Call: s.Calls, ContextRevision: revision, Usage: reported.Clone()}
	s.Latest = &observation
	if reported != nil && reported.InputTokens != nil {
		s.InputTokens += *reported.InputTokens
	} else {
		s.MissingInputCalls++
	}
	if reported != nil && reported.OutputTokens != nil {
		s.OutputTokens += *reported.OutputTokens
	} else {
		s.MissingOutputCalls++
	}
	a.usage.mu.Unlock()
	eventCopy := observation
	eventCopy.Usage = observation.Usage.Clone()
	return a.report(eventCopy)
}
