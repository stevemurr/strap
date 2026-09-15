// Package ledger finds runs of consecutive ledger entries for reconciliation.
package ledger

// RunsWithTotal walks the ledger once keeping the running prefix sum and a
// count of every prefix sum seen so far. A run ending at the current entry
// sums to target exactly when an earlier prefix equals prefix-target.
func RunsWithTotal(amounts []int, target int) int {
	seen := make(map[int]int, len(amounts)+1)
	seen[0] = 1
	runs, prefix := 0, 0
	for _, a := range amounts {
		prefix += a
		runs += seen[prefix-target]
		seen[prefix]++
	}
	return runs
}
