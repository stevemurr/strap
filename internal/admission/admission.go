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
	mu        sync.Mutex
	suspended bool
	drained   chan struct{}
	leases    map[uint64]context.CancelFunc
	nextLease uint64
	sealed    bool
	active    int
	limit     int
	done      chan struct{}
	ctx       context.Context
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
	if g.suspended {
		g.mu.Unlock()
		return nil, nil, ErrSuspended
	}
	if g.active >= g.limit {
		g.mu.Unlock()
		return nil, nil, ErrBusy
	}
	g.active++
	run, cancel := context.WithCancel(ctx)
	if g.leases == nil {
		g.leases = make(map[uint64]context.CancelFunc)
	}
	g.nextLease++
	id := g.nextLease
	g.leases[id] = cancel
	g.mu.Unlock()
	stop := context.AfterFunc(g.ctx, cancel)
	var once sync.Once
	return run, func() {
		once.Do(func() {
			stop()
			cancel()
			g.mu.Lock()
			g.active--
			delete(g.leases, id)
			if g.suspended && g.active == 0 {
				close(g.drained)
			}
			if g.sealed && g.active == 0 {
				close(g.done)
			}
			g.mu.Unlock()
		})
	}, nil
}

var ErrSuspended = errors.New("session interruption is in progress")

// Suspend fences new leases and cancels admitted operation contexts without
// terminating the gate. Operations retain ownership until they return; the
// channel acknowledges their settlement.
func (g *Gate) Suspend() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.suspended {
		g.suspended = true
		g.drained = make(chan struct{})
		for _, cancel := range g.leases {
			cancel()
		}
		if g.active == 0 {
			close(g.drained)
		}
	}
	return g.drained
}

func (g *Gate) Resume() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.suspended = false
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
