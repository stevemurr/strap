// Package scores computes progress statistics for the coaching app.
package scores

import "sort"

// LongestImprovement keeps tails[k] as the smallest possible last score of an
// increasing subsequence of length k+1. Each score replaces the first tail
// that is not smaller than it, or extends the array, so the array length is
// the answer and every step is a binary search.
func LongestImprovement(scores []int) int {
	tails := make([]int, 0, 16)
	for _, s := range scores {
		i := sort.SearchInts(tails, s)
		if i == len(tails) {
			tails = append(tails, s)
		} else {
			tails[i] = s
		}
	}
	return len(tails)
}
