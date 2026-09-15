// Package skyline sizes billboards that fit under a row of building slots.
package skyline

// LargestBillboard keeps a stack of slot indexes whose heights increase from
// bottom to top. When a lower slot arrives, every taller slot on the stack is
// popped and its rectangle is closed: the popped slot's height times the span
// between the new stack top and the current index. A sentinel height of zero
// at the end drains the stack, so each slot is pushed and popped exactly once.
func LargestBillboard(heights []int) int {
	best := 0
	stack := make([]int, 0, len(heights))
	for i := 0; i <= len(heights); i++ {
		h := 0
		if i < len(heights) {
			h = heights[i]
		}
		for len(stack) > 0 && heights[stack[len(stack)-1]] >= h {
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			left := -1
			if len(stack) > 0 {
				left = stack[len(stack)-1]
			}
			if area := heights[top] * (i - left - 1); area > best {
				best = area
			}
		}
		stack = append(stack, i)
	}
	return best
}
