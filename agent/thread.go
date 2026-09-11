package agent

import (
	"fmt"
	"sync"

	"github.com/stevemurr/strap/provider"
)

const (
	DefaultTranscriptLimit = 20
	MaxTranscriptLimit     = 100
)

// TranscriptQuery selects a chronological page from the append-only thread.
// Before is an exclusive, one-based position; zero selects the latest messages.
type TranscriptQuery struct {
	Limit  int
	Before uint64
}

type TranscriptEntry struct {
	Position uint64           `json:"position"`
	Message  provider.Message `json:"message"`
}

type TranscriptPage struct {
	Entries    []TranscriptEntry `json:"entries"`
	HasEarlier bool              `json:"has_earlier"`
}

// thread is the agent's sole conversation history. Stored messages are immutable;
// neither a provider nor an inspector receives aliases into retained content.
type thread struct {
	mu       sync.RWMutex
	messages []provider.Message
}

func (t *thread) append(m provider.Message) {
	owned := provider.CopyMessages([]provider.Message{m})[0]
	t.mu.Lock()
	t.messages = append(t.messages, owned)
	t.mu.Unlock()
}

func (t *thread) requestMessages() []provider.Message {
	t.mu.RLock()
	// Copy the outer slice under the lock; immutable payloads can be cloned outside.
	messages := append([]provider.Message(nil), t.messages...)
	t.mu.RUnlock()
	return provider.CopyMessages(messages)
}

func (t *thread) snapshot(q TranscriptQuery) (TranscriptPage, error) {
	if q.Limit == 0 {
		q.Limit = DefaultTranscriptLimit
	}
	if q.Limit < 1 || q.Limit > MaxTranscriptLimit {
		return TranscriptPage{}, fmt.Errorf("transcript limit must be between 1 and %d", MaxTranscriptLimit)
	}
	t.mu.RLock()
	end := len(t.messages)
	if q.Before != 0 {
		if q.Before > uint64(end)+1 {
			t.mu.RUnlock()
			return TranscriptPage{}, fmt.Errorf("transcript position %d is beyond the thread", q.Before)
		}
		end = int(q.Before - 1)
	}
	start := max(0, end-q.Limit)
	messages := append([]provider.Message(nil), t.messages[start:end]...)
	t.mu.RUnlock()
	messages = provider.CopyMessages(messages)
	page := TranscriptPage{Entries: make([]TranscriptEntry, len(messages)), HasEarlier: start > 0}
	for i, m := range messages {
		page.Entries[i] = TranscriptEntry{Position: uint64(start + i + 1), Message: m}
	}
	return page, nil
}

// Transcript reads history without waiting for model/tool execution or consuming
// inbox input. It remains available after the agent stops.
func (a *Agent) Transcript(q TranscriptQuery) (TranscriptPage, error) {
	return a.thread.snapshot(q)
}
