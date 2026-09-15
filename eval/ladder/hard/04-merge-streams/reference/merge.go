// Package feeds merges per-account activity feeds into one timeline.
package feeds

import "container/heap"

// head is the next unconsumed element of one stream.
type head struct {
	value  int
	stream int
	index  int
}

type heads []head

func (h heads) Len() int           { return len(h) }
func (h heads) Less(i, j int) bool { return h[i].value < h[j].value }
func (h heads) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *heads) Push(x any)        { *h = append(*h, x.(head)) }
func (h *heads) Pop() any          { old := *h; x := old[len(old)-1]; *h = old[:len(old)-1]; return x }

// Merge keeps a min-heap holding the head of every non-empty stream. Each
// step takes the smallest head into the result and replaces it with the next
// element of the same stream, so every element costs O(log k).
func Merge(streams [][]int) []int {
	total := 0
	h := make(heads, 0, len(streams))
	for i, s := range streams {
		total += len(s)
		if len(s) > 0 {
			h = append(h, head{value: s[0], stream: i})
		}
	}
	heap.Init(&h)
	out := make([]int, 0, total)
	for len(h) > 0 {
		top := h[0]
		out = append(out, top.value)
		s := streams[top.stream]
		if top.index+1 < len(s) {
			h[0] = head{value: s[top.index+1], stream: top.stream, index: top.index + 1}
			heap.Fix(&h, 0)
		} else {
			heap.Pop(&h)
		}
	}
	return out
}
