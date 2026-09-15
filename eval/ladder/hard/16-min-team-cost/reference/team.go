// Package hiring assembles the cheapest team that satisfies every hire's
// minimum wage under a shared pay rate.
package hiring

import (
	"container/heap"
	"math"
	"sort"
)

// MinTeamCost sorts the workers by wage/quality ratio. Whoever has the largest
// ratio on the team sets the rate, so each worker in that order is tried as
// the rate setter with the k-1 smallest qualities among the earlier workers,
// which a max-heap of size k maintains in O(log k) per step.
func MinTeamCost(quality, wage []int, k int) float64 {
	n := len(quality)
	if k < 1 || k > n {
		return 0
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		// wage[a]/quality[a] < wage[b]/quality[b] without division.
		return wage[order[a]]*quality[order[b]] < wage[order[b]]*quality[order[a]]
	})
	best := math.Inf(1)
	sum := 0
	largest := &maxHeap{}
	for _, i := range order {
		heap.Push(largest, quality[i])
		sum += quality[i]
		if largest.Len() > k {
			sum -= heap.Pop(largest).(int)
		}
		if largest.Len() == k {
			cost := float64(sum) * float64(wage[i]) / float64(quality[i])
			if cost < best {
				best = cost
			}
		}
	}
	return best
}

type maxHeap []int

func (h maxHeap) Len() int           { return len(h) }
func (h maxHeap) Less(i, j int) bool { return h[i] > h[j] }
func (h maxHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *maxHeap) Push(x any)        { *h = append(*h, x.(int)) }
func (h *maxHeap) Pop() any          { old := *h; x := old[len(old)-1]; *h = old[:len(old)-1]; return x }
