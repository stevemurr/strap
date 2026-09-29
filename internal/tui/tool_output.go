package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/stevemurr/strap/tool"
)

// Decode only the built-in result envelopes. The recorded result is unchanged;
// unfamiliar or malformed results remain literal text in the view.
func (d *toolDisplay) nativeOutput(raw string) {
	if d.languageOutput(raw) {
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil {
		return
	}
	switch d.name {
	case "shell":
		var result tool.ShellResult
		if fields["output"] == nil || fields["started"] == nil || json.Unmarshal([]byte(raw), &result) != nil {
			return
		}
		d.result = boundedToolText(result.Output, 32768)
		var failures []string
		if result.ExitCode != nil && *result.ExitCode != 0 {
			failures = append(failures, fmt.Sprintf("exit %d", *result.ExitCode))
		}
		if result.TimedOut {
			failures = append(failures, "timed out")
		}
		if result.Cancelled {
			failures = append(failures, "cancelled")
		}
		for _, err := range []string{result.StartError, result.WaitError, result.CleanupError} {
			if err != "" {
				failures = append(failures, err)
			}
		}
		d.failure = boundedToolText(strings.Join(failures, " · "), 2048)
		if result.Truncated {
			d.notice = "Output truncated by shell"
		}
		if result.OutputIncomplete {
			d.notice = strings.TrimPrefix(d.notice+" · Output stream incomplete", " · ")
		}
	case "read_file":
		var result tool.ReadFileResult
		if fields["content"] == nil || json.Unmarshal([]byte(raw), &result) != nil {
			return
		}
		d.result = boundedToolText(result.Content, 32768)
		d.numbered = true
		if result.Path != "" {
			d.path = safeText(result.Path)
		}
		if result.More || result.Truncated {
			d.notice = "Partial file view"
		}
	case "web_search":
		var result tool.WebSearchResult
		if fields["results"] == nil || json.Unmarshal([]byte(raw), &result) != nil {
			return
		}
		var listing []string
		pages := 0
		for _, h := range result.Results {
			hit := searchHit{title: inlineText(h.Title), url: inlineText(h.URL), snippet: inlineText(h.Snippet), page: utf8.RuneCountInString(h.Content), more: h.Truncated}
			if hit.page > 0 {
				pages++
			}
			d.hits = append(d.hits, hit)
			listing = append(listing, hit.title+"\n"+hit.url)
		}
		d.result = strings.Join(listing, "\n")
		if pages > 0 {
			d.notice = fmt.Sprintf("Page text for %d of %d results", pages, len(d.hits))
		}
	case "open_url":
		var result tool.OpenURLResult
		if fields["content"] == nil || fields["final_url"] == nil || json.Unmarshal([]byte(raw), &result) != nil {
			return
		}
		text := boundedToolText(result.Content, 32768)
		d.page = &pageView{title: inlineText(result.Title), url: inlineText(result.URL), finalURL: inlineText(result.FinalURL), text: text, chars: utf8.RuneCountInString(result.Content), more: result.Truncated, cut: result.DocumentTruncated, links: len(result.Links), moreLinks: result.LinksTruncated, rendered: result.Rendered}
		d.result = d.page.title + "\n" + text
	case "write_file":
		var result tool.WriteFileResult
		if fields["bytes_written"] != nil && json.Unmarshal([]byte(raw), &result) == nil {
			d.result = fmt.Sprintf("Wrote %d bytes", result.BytesWritten)
		}
	case "edit_file":
		var result tool.EditFileResult
		if fields["replacements"] != nil && json.Unmarshal([]byte(raw), &result) == nil {
			d.result = fmt.Sprintf("%d replacement(s)", result.Replacements)
		}
		var lines tool.EditLinesResult
		if fields["view"] != nil && json.Unmarshal([]byte(raw), &lines) == nil {
			d.result = boundedToolText(lines.View, 8192)
		}
	}
}
