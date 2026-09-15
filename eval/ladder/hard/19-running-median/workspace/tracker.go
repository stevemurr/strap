// Package latency tracks the running median of a stream of latency samples.
package latency

// Tracker keeps every sample added so far and reports their median.
type Tracker struct{}

// New returns an empty tracker.
func New() *Tracker {
	panic("not implemented")
}

// Add records one sample.
func (t *Tracker) Add(sample int) {
	panic("not implemented")
}

// Median returns the median of all samples added so far, the mean of the two
// middle values for an even count, and 0 when there are no samples.
func (t *Tracker) Median() float64 {
	panic("not implemented")
}
