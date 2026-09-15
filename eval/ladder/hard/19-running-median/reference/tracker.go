// Package latency tracks the running median of a stream of latency samples.
package latency

import "container/heap"

// Tracker keeps the lower half of the samples in a max-heap and the upper half
// in a min-heap. The lower half holds the extra element when the count is odd,
// so the median is always at the top of one or both heaps.
type Tracker struct {
	lower maxHeap
	upper minHeap
}

// New returns an empty tracker.
func New() *Tracker {
	return &Tracker{}
}

// Add records one sample, pushing it into the half it belongs to and moving
// one element across if the halves fall out of balance.
func (t *Tracker) Add(sample int) {
	if len(t.lower) == 0 || sample <= t.lower[0] {
		heap.Push(&t.lower, sample)
	} else {
		heap.Push(&t.upper, sample)
	}
	if len(t.lower) > len(t.upper)+1 {
		heap.Push(&t.upper, heap.Pop(&t.lower))
	} else if len(t.upper) > len(t.lower) {
		heap.Push(&t.lower, heap.Pop(&t.upper))
	}
}

// Median returns the median of all samples added so far, the mean of the two
// middle values for an even count, and 0 when there are no samples.
func (t *Tracker) Median() float64 {
	switch {
	case len(t.lower) == 0:
		return 0
	case len(t.lower) > len(t.upper):
		return float64(t.lower[0])
	default:
		return float64(t.lower[0]+t.upper[0]) / 2
	}
}

type maxHeap []int

func (h maxHeap) Len() int           { return len(h) }
func (h maxHeap) Less(i, j int) bool { return h[i] > h[j] }
func (h maxHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *maxHeap) Push(x any)        { *h = append(*h, x.(int)) }
func (h *maxHeap) Pop() any          { old := *h; x := old[len(old)-1]; *h = old[:len(old)-1]; return x }

type minHeap []int

func (h minHeap) Len() int           { return len(h) }
func (h minHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h minHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *minHeap) Push(x any)        { *h = append(*h, x.(int)) }
func (h *minHeap) Pop() any          { old := *h; x := old[len(old)-1]; *h = old[:len(old)-1]; return x }
