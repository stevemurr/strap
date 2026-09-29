package harness

import (
	"context"
	"errors"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/research"
	"github.com/stevemurr/strap/tool"
)

type DeepResearchConfig struct {
	Enabled bool            `json:"enabled"`
	Model   *ModelConfig    `json:"model,omitempty"`
	Limits  research.Config `json:"limits"`
}

type researchWebAdapter struct {
	web   *tool.Web
	actor identity.ActorID
}

// researchPageChars is how much of a page a research run reads, whether a
// search returned it or the browser rendered it.
const researchPageChars = 24000

// Search asks for each result's visible page text as well, so the run reads a
// result without a browser. Visible text rather than markdown: a claim cites
// an exact excerpt, and markdown runs link targets into the sentences quoted.
func (w researchWebAdapter) Search(ctx context.Context, query string) ([]research.Hit, error) {
	result, err := w.web.SearchWeb(ctx, query, 8, researchPageChars)
	if err != nil {
		return nil, err
	}
	out := []research.Hit{}
	for _, h := range result.Results {
		hit := research.Hit{Title: h.Title, URL: h.URL, Snippet: h.Snippet}
		if h.Content != "" {
			hit.Page = &research.Page{URL: h.URL, FinalURL: h.URL, Title: h.Title, ContentType: "text/plain", Text: h.Content, Truncated: h.Truncated}
		}
		out = append(out, hit)
	}
	return out, nil
}
func (w researchWebAdapter) Fetch(ctx context.Context, url string) (research.Page, error) {
	// Retain the returned selection immediately; browser cursor lifetime is irrelevant.
	p, err := w.web.OpenPage(ctx, w.actor, url, "", researchPageChars)
	if err != nil {
		return research.Page{}, err
	}
	return research.Page{URL: p.URL, FinalURL: p.FinalURL, Title: p.Title, ContentType: p.ContentType, Text: p.Content, Truncated: p.DocumentTruncated || p.Truncated}, nil
}
func (s *Session) recordResearch(ctx context.Context, e research.Event) error {
	if err := s.encoder.Publish(ctx, conversation.ResearchEvent{Event: e}); err != nil {
		s.log.Fail(err)
		return err
	}
	return nil
}
func (s *Session) ReadResearchReport(ctx context.Context, actor identity.ActorID, q research.ReadQuery) (inspection.ResearchPage, error) {
	if s.researchReads == nil {
		return inspection.ResearchPage{}, errors.New("deep research is not enabled")
	}
	page, err := s.researchReads.Read(ctx, actor, q)
	if !errors.Is(err, inspection.ErrClosed) {
		return page, err
	}
	// Execution close releases live readers, while the accepted log survives until
	// Dispose. Borrow a fresh reader and preserve the original cursor signing key.
	trace, err := s.Trace(ctx)
	if err != nil {
		return inspection.ResearchPage{}, err
	}
	defer trace.Close(context.Background())
	continuation := *s.researchReads.ProgressReader
	continuation.Reader = trace
	return (&inspection.ResearchReader{ProgressReader: &continuation}).Read(ctx, actor, q)
}
func (s *Session) researchReadTool() tool.Tool {
	return tool.GetResearchRun(func(ctx context.Context, c tool.Call, q research.ReadQuery) (tool.Result, error) {
		p, err := s.ReadResearchReport(ctx, c.Actor, q)
		if err != nil {
			return tool.Result{}, err
		}
		return modelJSON(p)
	})
}
