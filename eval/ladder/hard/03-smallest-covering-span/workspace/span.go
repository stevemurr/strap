// Package logspan finds the shortest stretch of an event log that contains a
// required mix of event types.
package logspan

// SmallestCoveringSpan returns the half-open range events[start:end] of
// smallest length that contains every entry of required, with repeats in
// required demanding repeats in the span. README.md defines the tie-breaking
// rule, the edge cases and the performance requirement.
func SmallestCoveringSpan(events []string, required []string) (start, end int, ok bool) {
	panic("not implemented")
}
