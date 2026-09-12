// Package admission coordinates session command lifetime without holding locks
// across application operations. Only top-level resource users acquire a lease.
package admission

import (
	"context"
	"errors"
	"sync"
)

var ErrClosed = errors.New("session is closing or closed")
var ErrBusy = errors.New("concurrent operation limit reached")

type Gate struct {
	mu     sync.Mutex
	sealed bool
	active int
	limit  int
	done   chan struct{}
	ctx    context.Context
}

func New(ctx context.Context) *Gate { return &Gate{ctx: ctx, done: make(chan struct{}), limit: 256} }
func (g *Gate) Begin(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	g.mu.Lock()
	if g.sealed || g.ctx.Err() != nil {
		g.mu.Unlock()
		return nil, nil, ErrClosed
	}
	if g.active >= g.limit {
		g.mu.Unlock()
		return nil, nil, ErrBusy
	}
	g.active++
	g.mu.Unlock()
	run, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(g.ctx, cancel)
	var once sync.Once
	return run, func() {
		once.Do(func() {
			stop()
			cancel()
			g.mu.Lock()
			g.active--
			if g.sealed && g.active == 0 {
				close(g.done)
			}
			g.mu.Unlock()
		})
	}, nil
}
func (g *Gate) Seal() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.sealed {
		g.sealed = true
		if g.active == 0 {
			close(g.done)
		}
	}
}
func (g *Gate) Wait(ctx context.Context) error {
	select {
	case <-g.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
