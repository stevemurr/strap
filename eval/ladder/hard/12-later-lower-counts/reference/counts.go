// Package pricehistory annotates a price series for the history page.
package pricehistory

import "slices"

// LaterLowerCounts compresses the prices to dense ranks, then sweeps from the
// last day to the first while a Fenwick tree counts the ranks seen so far. The
// prefix sum below a day's rank is exactly the number of later, lower days.
func LaterLowerCounts(prices []int) []int {
	out := make([]int, len(prices))
	if len(prices) == 0 {
		return out
	}
	ranks := slices.Clone(prices)
	slices.Sort(ranks)
	ranks = slices.Compact(ranks)
	tree := make([]int, len(ranks)+1)
	for i := len(prices) - 1; i >= 0; i-- {
		rank, _ := slices.BinarySearch(ranks, prices[i])
		rank++ // Fenwick trees are 1-based.
		sum := 0
		for j := rank - 1; j > 0; j -= j & -j {
			sum += tree[j]
		}
		out[i] = sum
		for j := rank; j < len(tree); j += j & -j {
			tree[j]++
		}
	}
	return out
}
