package tui

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// searchHit is a web_search result as the view shows it. The page text itself
// stays in the transcript; the view says how much of it the agent received.
type searchHit struct {
	title, url, snippet string
	page                int  // characters of the page's text returned with the result
	more                bool // the page continues past them
}

// searchSite names a result's site: its host without "www.".
func searchSite(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return raw
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

// searchRows lists a search's results one to a row: the title, then its site,
// which a long title is shortened to keep in view.
func searchRows(hits []searchHit, width int) []string {
	rows := make([]string, 0, len(hits))
	for _, h := range hits {
		site := " · " + searchSite(h.url)
		title := ansi.Truncate(h.title, max(1, width-ansi.StringWidth(site)), "…")
		rows = append(rows, ansi.Truncate(title+dimStyle.Render(site), width, "…"))
	}
	return rows
}

// searchDetail shows each result in full: its title, its address and how much
// page text came with it, and the start of its snippet.
func searchDetail(hits []searchHit, width int) []string {
	var rows []string
	for i, h := range hits {
		if i > 0 {
			rows = append(rows, "")
		}
		number := fmt.Sprintf("%d. ", i+1)
		indent := strings.Repeat(" ", len(number))
		inner := max(1, width-len(number))
		rows = append(rows, number+lipgloss.NewStyle().Bold(true).Render(ansi.Truncate(h.title, inner, "…")))
		page := "snippet only"
		if h.page > 0 {
			page = charCount(h.page) + " of page text"
			if h.more {
				page += ", continues"
			}
		}
		rows = append(rows, indent+ansi.Truncate(routeStyle.Render(h.url)+dimStyle.Render(" · "+page), inner, "…"))
		snippet := strings.Split(ansi.Wrap(h.snippet, inner, ""), "\n")
		if len(snippet) > 3 {
			snippet = append(snippet[:2], ansi.Truncate(snippet[2]+" …", inner, "…"))
		}
		for _, line := range snippet {
			if line != "" {
				rows = append(rows, indent+dimStyle.Render(line))
			}
		}
	}
	return rows
}

// charCount says how many characters, to one decimal place in thousands.
func charCount(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d chars", n)
	}
	return fmt.Sprintf("%.1fk chars", float64(n)/1000)
}

// pageView is an open_url result as the view shows it: the page's title and
// how much of its text the agent received, and the start of that text.
type pageView struct {
	title, url, finalURL, text string
	chars                      int  // characters of text the agent received
	more                       bool // a cursor reads on
	cut                        bool // the retention limit discarded the page's tail
	links                      int
	moreLinks                  bool
	rendered                   bool // a browser rendered it, rather than the search API extracting it
}

// pageTextRows is how much page text the expanded view shows; the agent's
// history has all of it.
const pageTextRows = 30

// pageMeta says how much of the page the agent received and what else came
// with it.
func pageMeta(p *pageView) string {
	parts := []string{charCount(p.chars) + " of text"}
	if p.more {
		parts[0] += ", continues"
	}
	if p.cut {
		parts = append(parts, "tail not kept")
	}
	if p.links > 0 {
		links := fmt.Sprintf("%d links", p.links)
		if p.moreLinks {
			links = fmt.Sprintf("%d+ links", p.links)
		}
		parts = append(parts, links)
	}
	if p.finalURL != "" && p.finalURL != p.url {
		parts = append(parts, "redirected to "+p.finalURL)
	}
	if p.rendered {
		parts = append(parts, "rendered in a browser")
	}
	return strings.Join(parts, " · ")
}

// pageRows are a read page's folded rows: its title, when it has one, then
// what came with it. An extracted page has none; the header names its URL.
func pageRows(p *pageView, width int) []string {
	meta := ansi.Truncate(dimStyle.Render(pageMeta(p)), width, "…")
	if p.title == "" {
		return []string{meta}
	}
	return []string{ansi.Truncate(p.title, width, "…"), meta}
}

// pageDetail adds the start of the page's text below its folded rows, without
// the blank line a browser's text puts between elements. It returns how many
// text rows it shows.
func pageDetail(p *pageView, width int) ([]string, int) {
	rows := pageRows(p, width)
	if p.title != "" {
		rows[0] = lipgloss.NewStyle().Bold(true).Render(rows[0])
	}
	var text []string
	for _, line := range strings.Split(p.text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		text = append(text, strings.Split(ansi.Hardwrap(line, width, true), "\n")...)
	}
	if len(text) == 0 {
		return rows, 0
	}
	shown := min(len(text), pageTextRows)
	rows = append(rows, "")
	for _, line := range text[:shown] {
		rows = append(rows, dimStyle.Render(line))
	}
	if hidden := len(text) - shown; hidden > 0 {
		rows = append(rows, dimStyle.Render(fmt.Sprintf("… +%d more lines · /transcript has the full text", hidden)))
	}
	return rows, shown
}
