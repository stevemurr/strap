// Package shelving counts box layouts for the warehouse planner.
package shelving

// Layouts walks the Fibonacci-style recurrence forward from width 0, keeping
// only the last two values, so width 90 costs 90 additions.
func Layouts(width int) int64 {
	if width < 0 {
		return 0
	}
	prev, cur := int64(0), int64(1) // Layouts(-1), Layouts(0).
	for i := 0; i < width; i++ {
		prev, cur = cur, prev+cur
	}
	return cur
}
