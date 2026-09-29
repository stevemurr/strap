package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

// A web search lists its results, title then site, instead of printing its
// JSON. Expanded, each result shows its address, how much page text came with
// it and its snippet. Titles are web content: their escape sequences never
// reach the terminal.
func TestWebSearchListsResults(t *testing.T) {
	m, _ := setup(t)
	m.entries = nil
	m.resize(120, 60)
	start := time.Date(2026, 9, 28, 20, 0, 0, 0, time.UTC)
	a := agent.ToolActivity{Call: provider.ToolCall{ID: "search", Name: "web_search", Arguments: []byte(`{"input":{"query":"latest go release","max_results":null}}`)}, StartedAt: start}
	m.observe(conversation.ToolEvent{Agent: "root", Activity: a})
	if view := ansi.Strip(m.viewport.View()); !strings.Contains(view, "○ Searching latest go release") {
		t.Fatalf("running search: %s", view)
	}
	result, _ := json.Marshal(tool.WebSearchResult{Query: "latest go release", Results: []tool.SearchHit{
		{Title: "Go 1.27 Release Notes", URL: "https://go.dev/doc/go1.27", Snippet: "The latest Go release, version 1.27.", Content: strings.Repeat("x", 4000), Truncated: true},
		{Title: "Go features \x1b[31mby version", URL: "https://www.antonz.org/go-features", Snippet: "A summary.", Content: "short page"},
		{Title: "Jobs", URL: "https://indeed.com/q-golang", Snippet: "Hiring."},
	}})
	a.FinishedAt, a.Result = start.Add(time.Second), tool.Text(string(result))
	m.observe(conversation.ToolEvent{Agent: "root", Activity: a})
	raw := m.viewport.View()
	view := ansi.Strip(raw)
	for _, want := range []string{"● Searched latest go release", "Go 1.27 Release Notes · go.dev", "Go features by version · antonz.org", "… +1 result (ctrl+t to expand)", "Page text for 2 of 3 results"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, `"results"`) || strings.Contains(raw, "\x1b[31mby") {
		t.Fatalf("printed JSON or a page's escape sequence:\n%q", raw)
	}
	expandActivityForTest(m, true)
	view = ansi.Strip(m.viewport.View())
	for _, want := range []string{"1. Go 1.27 Release Notes", "https://go.dev/doc/go1.27 · 4.0k chars of page text, continues", "│    The latest Go release, version 1.27.", "https://www.antonz.org/go-features · 10 chars of page text", "3. Jobs", "https://indeed.com/q-golang · snippet only", "collapse output (ctrl+t)"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Arguments") {
		t.Fatalf("repeated the query as arguments:\n%s", view)
	}
	assertFits(t, view, m.viewport.Width, 0)
}

// A read page shows its title and how much text the agent received, and how
// it was read, instead of printing its JSON. Expanded, it shows the start of that text without the
// browser's blank lines between elements; the page's escape sequences never
// reach the terminal. A cursor read says it read on.
func TestOpenURLShowsThePage(t *testing.T) {
	m, _ := setup(t)
	m.entries = nil
	m.resize(120, 80)
	start := time.Date(2026, 9, 28, 20, 0, 0, 0, time.UTC)
	a := agent.ToolActivity{Call: provider.ToolCall{ID: "open", Name: "open_url", Arguments: []byte(`{"input":{"url":"https://go.dev/doc/go1.26","max_chars":null,"cursor":null}}`)}, StartedAt: start}
	m.observe(conversation.ToolEvent{Agent: "root", Activity: a})
	if view := ansi.Strip(m.viewport.View()); !strings.Contains(view, "○ Opening https://go.dev/doc/go1.26") {
		t.Fatalf("running read: %s", view)
	}
	var text strings.Builder
	text.WriteString("Skip to Main Content\n\n\x1b[31mGo 1.26 Release Notes\n\n")
	for i := range 40 {
		fmt.Fprintf(&text, "line %d\n\n", i)
	}
	result, _ := json.Marshal(tool.OpenURLResult{URL: "https://go.dev/doc/go1.26", FinalURL: "https://go.dev/doc/go1.26/", Title: "Go 1.26 Release Notes", ContentType: "text/html", Content: text.String(), Links: make([]tool.WebLink, 200), LinksTruncated: true, Truncated: true, Rendered: true, NextCursor: "abc.300"})
	a.FinishedAt, a.Result = start.Add(time.Second), tool.Text(string(result))
	m.observe(conversation.ToolEvent{Agent: "root", Activity: a})
	raw := m.viewport.View()
	view := ansi.Strip(raw)
	for _, want := range []string{"● Opened https://go.dev/doc/go1.26", "└ Go 1.26 Release Notes", "400 chars of text, continues · 200+ links · redirected to https://go.dev/doc/go1.26/ · rendered in a browser", "… +30 lines of page text (ctrl+t to expand)"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, `"final_url"`) {
		t.Fatalf("printed JSON:\n%s", view)
	}
	expandActivityForTest(m, true)
	raw = m.viewport.View()
	view = ansi.Strip(raw)
	var rows []string
	for _, row := range strings.Split(view, "\n") {
		rows = append(rows, strings.TrimSpace(row))
	}
	joined := strings.Join(rows, "\n")
	for _, want := range []string{"│ Skip to Main Content\n│ Go 1.26 Release Notes\n│ line 0\n", "│ line 27\n│ … +12 more lines · /transcript has the full text", "collapse output (ctrl+t)"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(view, "line 28") || strings.Contains(view, "Arguments") || strings.Contains(raw, "\x1b[31mGo") {
		t.Fatalf("showed too much, repeated arguments, or passed an escape sequence:\n%q", raw)
	}
	assertFits(t, view, m.viewport.Width, 0)

	next := agent.ToolActivity{Call: provider.ToolCall{ID: "more", Name: "open_url", Arguments: []byte(`{"input":{"url":"https://go.dev/doc/go1.26","max_chars":null,"cursor":"abc.300"}}`)}, StartedAt: start}
	m.observe(conversation.ToolEvent{Agent: "root", Activity: next})
	if view := ansi.Strip(m.viewport.View()); !strings.Contains(view, "○ Reading more of https://go.dev/doc/go1.26") {
		t.Fatalf("running cursor read: %s", view)
	}
}

// A page the search API extracted has no title: its folded view is what came
// with it, under the header that names its URL.
func TestExtractedPageShowsWhatCameWithIt(t *testing.T) {
	m, _ := setup(t)
	m.entries = nil
	m.resize(120, 40)
	start := time.Date(2026, 9, 28, 20, 0, 0, 0, time.UTC)
	a := agent.ToolActivity{Call: provider.ToolCall{ID: "open", Name: "open_url", Arguments: []byte(`{"input":{"url":"https://go.dev/doc/go1.27","max_chars":null,"cursor":null,"render":null}}`)}, StartedAt: start}
	m.observe(conversation.ToolEvent{Agent: "root", Activity: a})
	result, _ := json.Marshal(tool.OpenURLResult{URL: "https://go.dev/doc/go1.27", FinalURL: "https://go.dev/doc/go1.27", ContentType: "text/markdown", Content: "# Go 1.27 Release Notes\n\nGeneric methods.", Links: make([]tool.WebLink, 3)})
	a.FinishedAt, a.Result = start.Add(time.Second), tool.Text(string(result))
	m.observe(conversation.ToolEvent{Agent: "root", Activity: a})
	view := ansi.Strip(m.viewport.View())
	if !strings.Contains(view, "● Opened https://go.dev/doc/go1.27") || !strings.Contains(view, "└ 41 chars of text · 3 links") || strings.Contains(view, "untitled") || strings.Contains(view, "rendered") {
		t.Fatalf("extracted page:\n%s", view)
	}
}
