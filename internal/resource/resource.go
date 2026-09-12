// Package resource tracks explicit ownership independently of execution interfaces.
package resource

import (
	"context"
	"errors"
	"fmt"
)

type Resource interface{ Close(context.Context) error }

type entry struct {
	name   string
	value  Resource
	closed bool
}

// Group is assembled before use. Close calls serialize; failed entries remain owned.
type Group struct {
	entries []entry
	gate    chan struct{}
}

func New() *Group { return &Group{gate: make(chan struct{}, 1)} }
func (g *Group) Add(name string, value Resource) {
	g.entries = append(g.entries, entry{name: name, value: value})
}
func (g *Group) Close(ctx context.Context) error {
	select {
	case g.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-g.gate }()
	var result error
	for i := len(g.entries) - 1; i >= 0; i-- {
		e := &g.entries[i]
		if e.closed {
			continue
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		if err := e.value.Close(ctx); err != nil {
			result = errors.Join(result, fmt.Errorf("close %s: %w", e.name, err))
		} else {
			e.closed = true
		}
	}
	return result
}
