// Package bookings validates a room's schedule for the meeting-room service.
package bookings

// HasConflict reports whether any two bookings, given as [start, end) pairs,
// overlap. README.md defines what counts as an overlap and the edge cases.
func HasConflict(bookings [][2]int) bool {
	panic("not implemented")
}
