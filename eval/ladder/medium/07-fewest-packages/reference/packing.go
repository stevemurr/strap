// Package packing chooses package sizes for warehouse orders.
package packing

// FewestPackages fills best[q] for every quantity from 1 upwards: the fewest
// packages for q is one more than the fewest for q minus some size, taking the
// minimum over sizes that fit. Unreachable quantities stay at -1.
func FewestPackages(sizes []int, quantity int) int {
	if quantity < 0 {
		return -1
	}
	best := make([]int, quantity+1)
	for q := 1; q <= quantity; q++ {
		best[q] = -1
		for _, s := range sizes {
			if s > q || best[q-s] < 0 {
				continue
			}
			if best[q] < 0 || best[q-s]+1 < best[q] {
				best[q] = best[q-s] + 1
			}
		}
	}
	return best[quantity]
}
