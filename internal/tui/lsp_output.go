package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stevemurr/strap/lsp"
)

// Language results are self-contained, so recorded sessions need no live server.
func (d *toolDisplay) languageOutput(raw string) bool {
	if !strings.HasPrefix(d.name, "lsp_") {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil {
		return true
	}
	if d.name == "lsp_status" {
		var status lsp.Status
		if fields["servers"] == nil || json.Unmarshal([]byte(raw), &status) != nil {
			return true
		}
		var lines []string
		for _, s := range status.Servers {
			lines = append(lines, strings.TrimSpace(s.ID+" · "+s.State+" · "+s.Root+" "+s.Version))
			if s.Error != "" {
				lines = append(lines, s.Error)
			}
		}
		d.result = boundedToolText(strings.Join(lines, "\n"), 32768)
		return true
	}
	var items []lsp.Item
	var metadata lsp.Metadata
	more, truncated := false, false
	if d.name == "lsp_inspect" {
		var result lsp.Inspection
		if fields["location"] == nil || json.Unmarshal([]byte(raw), &result) != nil {
			return true
		}
		items, metadata, truncated = []lsp.Item{result.Location}, result.Metadata, result.Truncated
		d.result = boundedToolText(result.Documentation, 16000)
	} else {
		var result lsp.Page
		if fields["items"] == nil || json.Unmarshal([]byte(raw), &result) != nil {
			return true
		}
		items, metadata, more, truncated = result.Items, result.Metadata, result.NextCursor != "", result.Truncated
		d.result = ""
	}
	var lines []string
	if d.result != "" {
		lines = append(lines, d.result)
	}
	for _, item := range items {
		loc := item.Path
		if item.Selection != nil {
			loc += fmt.Sprintf(":%d:%d", item.Selection.Start.Line, item.Selection.Start.Column)
		}
		label := strings.TrimSpace(strings.Join([]string{item.Name, item.Kind, item.Severity, item.Message}, " "))
		lines = append(lines, strings.Repeat("  ", min(item.Depth, 8))+loc+"  "+label)
		if item.Excerpt != "" {
			lines = append(lines, item.Excerpt)
		}
	}
	for _, check := range metadata.Checks {
		lines = append(lines, fmt.Sprintf("%s · %s · %d diagnostic(s)", check.Path, check.Freshness, check.Count))
	}
	if len(lines) == 0 {
		lines = append(lines, "No results")
	}
	d.result = boundedToolText(strings.Join(lines, "\n"), 32768)
	notices := []string{metadata.Freshness}
	if metadata.Partial {
		notices = append(notices, "partial coverage")
	}
	if more {
		notices = append(notices, "more results available")
	}
	if truncated {
		notices = append(notices, "capture truncated")
	}
	notices = append(notices, metadata.Issues...)
	d.notice = boundedToolText(strings.Join(notices, " · "), 2048)
	return true
}
