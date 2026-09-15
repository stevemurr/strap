// Package bisect locates the first failing build for the CI bisection tool.
package bisect

// FirstFailing binary searches the range 1..n. Because failing is monotonic,
// a failing midpoint means the answer is at or before it and a passing
// midpoint means it is after, so each probe halves the range and at most
// ceil(log2(n)) probes are needed.
func FirstFailing(n int, failing func(build int) bool) int {
	lo, hi := 1, n
	for lo < hi {
		mid := lo + (hi-lo)/2
		if failing(mid) {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo
}
