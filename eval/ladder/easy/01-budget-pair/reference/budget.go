// Package budgetpair finds two catalog items that spend a gift card exactly.
package budgetpair

// PairForBudget scans once, remembering the first index of every price seen so
// far. The first j with a stored complement is the smallest qualifying j, and
// the stored index is the smallest i for that j.
func PairForBudget(prices []int, budget int) (i, j int, ok bool) {
	seen := make(map[int]int, len(prices))
	for j, p := range prices {
		if i, found := seen[budget-p]; found {
			return i, j, true
		}
		if _, dup := seen[p]; !dup {
			seen[p] = j
		}
	}
	return 0, 0, false
}
