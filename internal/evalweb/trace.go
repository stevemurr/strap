package evalweb

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"sort"
	"time"
)

// traceQuery selects a page of envelopes from one trace file.
type traceQuery struct {
	after int      // only sequences above this
	limit int      // page size; defaults to 200
	kinds []string // include only these kinds; empty means all but output_delta
	agent string   // include only this agent when set
}

// TraceEvent is one envelope with its payload left as written. The page
// renders kinds it knows and shows the JSON for the rest.
type TraceEvent struct {
	Sequence int             `json:"sequence"`
	Kind     string          `json:"kind"`
	Agent    string          `json:"agent,omitempty"`
	Time     time.Time       `json:"time"`
	Payload  json.RawMessage `json:"payload"`
}

// TracePage is one slice of a trace with the counts the filter bar needs.
type TracePage struct {
	Events []TraceEvent   `json:"events"`
	Kinds  map[string]int `json:"kinds"`
	Agents []string       `json:"agents"`
	Total  int            `json:"total"`
	Next   int            `json:"next,omitempty"`
	First  time.Time      `json:"first"`
	Last   time.Time      `json:"last"`
}

// readTrace scans the whole file once: the histogram covers every line while
// the page covers only the matching ones after the cursor. Traces run to a
// few megabytes, so one pass per page request is cheap enough.
func readTrace(path string, q traceQuery) (*TracePage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if q.limit <= 0 {
		q.limit = 200
	}
	include := map[string]bool{}
	for _, k := range q.kinds {
		include[k] = true
	}
	page := &TracePage{Events: []TraceEvent{}, Kinds: map[string]int{}}
	agents := map[string]bool{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 64<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var e TraceEvent
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		page.Total++
		page.Kinds[e.Kind]++
		if e.Agent != "" {
			agents[e.Agent] = true
		}
		if page.First.IsZero() || e.Time.Before(page.First) {
			page.First = e.Time
		}
		if e.Time.After(page.Last) {
			page.Last = e.Time
		}
		if e.Sequence <= q.after {
			continue
		}
		if len(include) > 0 && !include[e.Kind] || len(include) == 0 && e.Kind == "output_delta" {
			continue
		}
		if q.agent != "" && e.Agent != q.agent {
			continue
		}
		if len(page.Events) == q.limit {
			if page.Next == 0 {
				page.Next = page.Events[len(page.Events)-1].Sequence
			}
			continue
		}
		e.Payload = append(json.RawMessage(nil), e.Payload...)
		page.Events = append(page.Events, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	for a := range agents {
		page.Agents = append(page.Agents, a)
	}
	sort.Strings(page.Agents)
	return page, nil
}
