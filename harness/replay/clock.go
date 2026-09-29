package replay

import (
	"sync"
	"time"
)

// virtualClock is the time a replayed session schedules progress notices by.
// It stands at the recording's start and moves only when the replay reaches a
// recorded point, to that point's recorded time, so a notice the recording
// sent at some moment becomes due at the same place in the replay, however
// fast the recorded outputs are served.
type virtualClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters map[*waiter]bool
}

type waiter struct {
	at    time.Time
	fired chan time.Time
}

func newVirtualClock(start time.Time) *virtualClock {
	return &virtualClock{now: start, waiters: map[*waiter]bool{}}
}

func (c *virtualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *virtualClock) Timer(at time.Time) (<-chan time.Time, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := &waiter{at: at, fired: make(chan time.Time, 1)}
	if !at.After(c.now) {
		w.fired <- c.now
		return w.fired, func() {}
	}
	c.waiters[w] = true
	return w.fired, func() {
		c.mu.Lock()
		delete(c.waiters, w)
		c.mu.Unlock()
	}
}

// advance moves the clock forward to t, firing every timer due by then.
func (c *virtualClock) advance(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !t.After(c.now) {
		return
	}
	c.now = t
	for w := range c.waiters {
		if !w.at.After(t) {
			w.fired <- t
			delete(c.waiters, w)
		}
	}
}
