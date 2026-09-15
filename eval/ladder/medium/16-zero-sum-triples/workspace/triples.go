// Package reconcile finds groups of ledger deltas that cancel out.
package reconcile

// ZeroSumTriples returns every distinct value triple a <= b <= c, taken from
// three different positions, with a+b+c == 0, sorted ascending. README.md
// defines the edge cases and the performance requirement.
func ZeroSumTriples(deltas []int) [][3]int {
	panic("not implemented")
}
