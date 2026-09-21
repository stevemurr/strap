package lsp

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/bmatcuk/doublestar/v4"
)

type fileWatcher struct {
	pattern string
	kind    int
}
type registration struct {
	ID      string `json:"id"`
	Method  string `json:"method"`
	Options struct {
		Watchers []struct {
			Glob json.RawMessage `json:"globPattern"`
			Kind *int            `json:"kind"`
		} `json:"watchers"`
	} `json:"registerOptions"`
}

func (c *client) register(regs []registration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.registrations)+len(regs) > 64 {
		return fmt.Errorf("watch registration limit exceeded")
	}
	next := map[string][]fileWatcher{}
	count := 0
	for _, existing := range c.registrations {
		count += len(existing)
	}
	for _, r := range regs {
		if r.Method != "workspace/didChangeWatchedFiles" || r.ID == "" {
			return fmt.Errorf("unsupported dynamic registration")
		}
		for _, w := range r.Options.Watchers {
			count++
			if count > 512 {
				return fmt.Errorf("watch pattern limit exceeded")
			}
			var pattern string
			// RelativePattern is not advertised; it requires explicit base URI handling.
			if json.Unmarshal(w.Glob, &pattern) != nil || len(pattern) > 4096 {
				return fmt.Errorf("unsupported watch glob")
			}
			if !doublestar.ValidatePattern(pattern) {
				return fmt.Errorf("invalid watch glob")
			}
			kind := 7
			if w.Kind != nil {
				kind = *w.Kind
			}
			if kind < 1 || kind > 7 {
				return fmt.Errorf("invalid watch kind")
			}
			next[r.ID] = append(next[r.ID], fileWatcher{pattern, kind})
		}
	}
	for id, watchers := range next {
		c.registrations[id] = watchers
	}
	return nil
}
func (c *client) watchedChanges(changes []map[string]any) []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []map[string]any{}
	for _, change := range changes {
		uri, _ := change["uri"].(string)
		path, err := uriPath(uri)
		if err != nil {
			continue
		}
		kind, _ := change["type"].(int)
		if kind < 1 || kind > 3 {
			continue
		}
		path = filepath.ToSlash(path)
		matched := false
		for _, watchers := range c.registrations {
			for _, w := range watchers {
				if w.kind&(1<<(kind-1)) != 0 && doublestar.MatchUnvalidated(w.pattern, path) {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if matched {
			out = append(out, change)
		}
	}
	return out
}
