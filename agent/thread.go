package agent

import (
	"cmp"
	"errors"
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
var ErrInvalidQuery = errors.New("invalid agent history query")

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
	revision uint64
}

func (t *thread) append(m provider.Message) uint64 {
	owned := provider.CopyMessages([]provider.Message{m})[0]
	t.mu.Lock()
	t.messages = append(t.messages, owned)
	t.revision++
	revision := t.revision
	t.mu.Unlock()
	return revision
}

// messagesAt reads an exact historical request boundary, even if execution has
// advanced since the host received its event. Revisions are append positions.
func (t *thread) messagesAt(revision uint64) ([]provider.Message, error) {
	t.mu.RLock()
	if revision == 0 || revision > t.revision {
		t.mu.RUnlock()
		return nil, fmt.Errorf("%w: invalid context revision %d", ErrInvalidQuery, revision)
	}
	messages := append([]provider.Message(nil), t.messages[:revision]...)
	t.mu.RUnlock()
	return provider.CopyMessages(messages), nil
}

func (t *thread) requestMessages() ([]provider.Message, uint64) {
	t.mu.RLock()
	// Copy the outer slice under the lock; immutable payloads can be cloned outside.
	messages := append([]provider.Message(nil), t.messages...)
	revision := t.revision
	t.mu.RUnlock()
	return provider.CopyMessages(messages), revision
}

// pendingCalls reads only the final assistant batch and its results. Interrupting
// a long conversation must not clone the entire transcript to settle that batch.
func (t *thread) pendingCalls() []provider.ToolCall {
	t.mu.RLock()
	defer t.mu.RUnlock()
	completed := map[string]int{}
	for i := len(t.messages) - 1; i >= 0; i-- {
		m := t.messages[i]
		if m.Role == "tool" {
			completed[m.ToolCallID]++
		}
		if m.Role != "assistant" {
			continue
		}
		var pending []provider.ToolCall
		for _, call := range m.ToolCalls {
			if completed[call.ID] > 0 {
				completed[call.ID]--
				continue
			}
			pending = append(pending, call)
		}
		return provider.CopyCalls(pending)
	}
	return nil
}

func (t *thread) snapshot(q TranscriptQuery) (TranscriptPage, error) {
	q.Limit = cmp.Or(q.Limit, DefaultTranscriptLimit)
	if q.Limit < 1 || q.Limit > MaxTranscriptLimit {
		return TranscriptPage{}, fmt.Errorf("%w: transcript limit must be between 1 and %d", ErrInvalidQuery, MaxTranscriptLimit)
	}
	t.mu.RLock()
	end := len(t.messages)
	if q.Before != 0 {
		if q.Before > uint64(end)+1 {
			t.mu.RUnlock()
			return TranscriptPage{}, fmt.Errorf("%w: transcript position %d is beyond the thread", ErrInvalidQuery, q.Before)
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
