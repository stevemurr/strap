// Package packets analyses packet id sequences captured by the network tool.
package packets

// LongestDistinctStreak slides a window over the ids. The window start jumps
// past the previous occurrence of the current id whenever that occurrence is
// still inside the window, so every id is visited once.
func LongestDistinctStreak(ids []int) int {
	last := make(map[int]int)
	best, start := 0, 0
	for i, id := range ids {
		if prev, seen := last[id]; seen && prev >= start {
			start = prev + 1
		}
		last[id] = i
		best = max(best, i-start+1)
	}
	return best
}
