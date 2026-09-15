// Package uptime summarises health-check history for the status page.
package uptime

// Longest keeps the length of the current run of passing checks and the best
// run seen so far, resetting the current run at every failed check.
func Longest(up []bool) int {
	best, run := 0, 0
	for _, ok := range up {
		if !ok {
			run = 0
			continue
		}
		run++
		if run > best {
			best = run
		}
	}
	return best
}
