// Package rollingpeak computes the peak reading of every fixed-size window for
// the metrics dashboard.
package rollingpeak

// PeakPerWindow returns the largest reading in each window of k consecutive
// readings, ordered by the window's start. README.md defines the edge cases
// and the performance requirement.
func PeakPerWindow(readings []int, k int) []int {
	panic("not implemented")
}
