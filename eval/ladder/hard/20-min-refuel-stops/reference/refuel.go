// Package roadtrip plans fuel stops for highway deliveries.
package roadtrip

import "container/heap"

// MinRefuelStops drives as far as the current fuel allows, collecting every
// station passed into a max-heap. Whenever the destination is still out of
// reach it retroactively stops at the passed station with the most fuel;
// exchanging any chosen stop for a larger passed one never hurts, so this
// greedy choice is optimal.
func MinRefuelStops(target, startFuel int, stations [][2]int) int {
	reach := startFuel
	passed := &maxHeap{}
	stops, next := 0, 0
	for reach < target {
		for next < len(stations) && stations[next][0] <= reach {
			heap.Push(passed, stations[next][1])
			next++
		}
		if passed.Len() == 0 {
			return -1
		}
		reach += heap.Pop(passed).(int)
		stops++
	}
	return stops
}

type maxHeap []int

func (h maxHeap) Len() int           { return len(h) }
func (h maxHeap) Less(i, j int) bool { return h[i] > h[j] }
func (h maxHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *maxHeap) Push(x any)        { *h = append(*h, x.(int)) }
func (h *maxHeap) Pop() any          { old := *h; x := old[len(old)-1]; *h = old[:len(old)-1]; return x }
