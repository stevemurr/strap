// Package builds schedules build targets so that every dependency is built
// before the targets that need it.
package builds

import (
	"container/heap"
	"errors"
	"fmt"
)

// Order runs Kahn's algorithm with a min-heap of ready targets keyed by name.
// Popping the smallest ready name at every step yields the lexicographically
// smallest valid order. Targets left unplaced at the end lie on a cycle.
func Order(targets []string, deps [][2]string) ([]string, error) {
	index := make(map[string]int, len(targets))
	for i, name := range targets {
		if _, dup := index[name]; dup {
			return nil, fmt.Errorf("builds: duplicate target %q", name)
		}
		index[name] = i
	}
	next := make([][]int, len(targets))
	indegree := make([]int, len(targets))
	for _, d := range deps {
		before, ok := index[d[0]]
		if !ok {
			return nil, fmt.Errorf("builds: unknown target %q", d[0])
		}
		after, ok := index[d[1]]
		if !ok {
			return nil, fmt.Errorf("builds: unknown target %q", d[1])
		}
		next[before] = append(next[before], after)
		indegree[after]++
	}
	ready := &readyHeap{names: targets}
	for i, n := range indegree {
		if n == 0 {
			ready.items = append(ready.items, i)
		}
	}
	heap.Init(ready)
	out := make([]string, 0, len(targets))
	for ready.Len() > 0 {
		i := heap.Pop(ready).(int)
		out = append(out, targets[i])
		for _, j := range next[i] {
			indegree[j]--
			if indegree[j] == 0 {
				heap.Push(ready, j)
			}
		}
	}
	if len(out) != len(targets) {
		return nil, errors.New("builds: dependency cycle")
	}
	return out, nil
}

// readyHeap orders target indexes by their names.
type readyHeap struct {
	names []string
	items []int
}

func (h *readyHeap) Len() int           { return len(h.items) }
func (h *readyHeap) Less(i, j int) bool { return h.names[h.items[i]] < h.names[h.items[j]] }
func (h *readyHeap) Swap(i, j int)      { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *readyHeap) Push(x any)         { h.items = append(h.items, x.(int)) }

func (h *readyHeap) Pop() any {
	last := len(h.items) - 1
	x := h.items[last]
	h.items = h.items[:last]
	return x
}
