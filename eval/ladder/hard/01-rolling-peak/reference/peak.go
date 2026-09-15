// Package rollingpeak computes the peak reading of every fixed-size window for
// the metrics dashboard.
package rollingpeak

// PeakPerWindow keeps a deque of candidate indexes whose readings decrease
// from front to back. The front is always the current window's peak; indexes
// that fall out of the window are dropped from the front, and smaller readings
// are dropped from the back when a larger one arrives.
func PeakPerWindow(readings []int, k int) []int {
	if k < 1 || k > len(readings) {
		return nil
	}
	out := make([]int, 0, len(readings)-k+1)
	deque := make([]int, 0, k)
	for i, v := range readings {
		for len(deque) > 0 && readings[deque[len(deque)-1]] <= v {
			deque = deque[:len(deque)-1]
		}
		deque = append(deque, i)
		if deque[0] <= i-k {
			deque = deque[1:]
		}
		if i >= k-1 {
			out = append(out, readings[deque[0]])
		}
	}
	return out
}
