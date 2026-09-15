// Package bookings validates a room's schedule for the meeting-room service.
package bookings

import (
	"cmp"
	"slices"
)

// HasConflict sorts a copy of the bookings by start time. After that a
// conflict can only exist between neighbours, so one pass comparing each start
// against the previous end finds it.
func HasConflict(bookings [][2]int) bool {
	sorted := slices.Clone(bookings)
	slices.SortFunc(sorted, func(x, y [2]int) int { return cmp.Compare(x[0], y[0]) })
	for i := 1; i < len(sorted); i++ {
		if sorted[i][0] < sorted[i-1][1] {
			return true
		}
	}
	return false
}
