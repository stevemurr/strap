package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// initialize establishes protocol readiness, not completion of background
// indexing. During a short cold-start window, retry empty semantic reads rather
// than turning an asynchronous configuration/load race into a false absence.
// Empty results after that window are normal; this is not a convergence barrier.
func (c *client) semanticCall(ctx context.Context, method string, params, result any) error {
	ctx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	cold := time.Now().Before(c.warmupUntil)
	var raw json.RawMessage
	retries := 0
	for {
		err := c.call(ctx, method, params, &raw)
		if err != nil {
			var issue *Error
			if !errors.As(err, &issue) || issue.Kind != "content_modified" || retries >= 3 {
				return err
			}
			// These are read-only requests; the manager rechecks source hashes
			// after the result. Bound retries if analysis keeps invalidating them.
			retries++
		} else {
			empty := emptySemanticResult(method, raw)
			if !empty || !cold || !time.Now().Before(c.warmupUntil) {
				c.mu.Lock()
				c.startupIncomplete = cold && empty
				c.mu.Unlock()
				return decodeRaw(raw, result)
			}
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func emptySemanticResult(method string, raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "[]" {
		return true
	}
	if method == "textDocument/hover" {
		var hover struct {
			Contents json.RawMessage `json:"contents"`
		}
		if json.Unmarshal(raw, &hover) == nil {
			return hoverText(hover.Contents) == ""
		}
	}
	return false
}
func (m *Manager) addReadiness(s *instance, meta *Metadata) {
	s.client.mu.Lock()
	busy := len(s.client.progress) > 0
	incomplete := s.client.startupIncomplete
	// The empty-result warning belongs to the preceding semantic request.
	// Do not carry it indefinitely into later diagnostic-only queries.
	s.client.startupIncomplete = false
	s.client.mu.Unlock()
	if busy || incomplete {
		appendIssue(meta, failure("indexing", "initial/background analysis may be incomplete; retry this query after indexing"))
	}
}

// Give servers a short debounce after synchronization, then honor their standard
// work-done progress before pulling diagnostics. The caller's diagnostic deadline
// remains authoritative; a timeout is pending analysis, not a clean report.
func (c *client) waitAnalysis(ctx context.Context, changed time.Time) bool {
	delay := time.Until(changed.Add(200 * time.Millisecond))
	if delay > 0 {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
	for {
		c.mu.Lock()
		busy := len(c.progress) > 0
		c.mu.Unlock()
		if !busy {
			return ctx.Err() == nil
		}
		select {
		case <-ctx.Done():
			return false
		case <-c.done:
			return false
		case <-c.changed:
		}
	}
}
