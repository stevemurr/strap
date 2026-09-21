package harness

import (
	"context"
	"sync"
	"time"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/lsp"
	"github.com/stevemurr/strap/tool"
)

type languageFeedback struct {
	manager *lsp.Manager
	mu      sync.Mutex
	seen    map[identity.ActorID]string
}
type languageTool struct {
	tool.Tool
	feedback *languageFeedback
}

func (f *languageFeedback) wrap(t tool.Tool) tool.Tool { return &languageTool{t, f} }
func (t *languageTool) InputContract() tool.Contract {
	return t.Tool.(interface{ InputContract() tool.Contract }).InputContract()
}
func (t *languageTool) Validate() error { return tool.ValidateTool(t.Tool) }
func (t *languageTool) BookkeepingParameters() []string {
	if b, ok := t.Tool.(interface{ BookkeepingParameters() []string }); ok {
		return b.BookkeepingParameters()
	}
	return nil
}
func (t *languageTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	result, err := t.Tool.Call(ctx, call)
	// Do not delay a committed edit for analysis. Only append an already received,
	// version-matched summary at a subsequent tool boundary, once per actor/change.
	brief, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	summary, summaryErr := t.feedback.manager.CachedSummary(brief)
	if summaryErr == nil && summary == "" {
		t.feedback.mu.Lock()
		delete(t.feedback.seen, call.Actor)
		t.feedback.mu.Unlock()
	}
	if summary != "" {
		f := t.feedback
		f.mu.Lock()
		changed := f.seen[call.Actor] != summary
		if len(f.seen) > 256 {
			clear(f.seen)
		}
		f.seen[call.Actor] = summary
		f.mu.Unlock()
		if changed {
			result.Content = append(result.Content, content.Text("Current cached language diagnostics (up to 5; requested files/build only; use lsp_diagnostics for details):\n"+summary)...)
		}
	}
	return result, err
}
