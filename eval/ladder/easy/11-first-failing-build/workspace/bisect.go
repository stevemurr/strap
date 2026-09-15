// Package bisect locates the first failing build for the CI bisection tool.
package bisect

// FirstFailing returns the smallest build in 1..n for which failing reports
// true, given that failing is monotonic and failing(n) is true. README.md
// defines the limit on how many times failing may be called.
func FirstFailing(n int, failing func(build int) bool) int {
	panic("not implemented")
}
