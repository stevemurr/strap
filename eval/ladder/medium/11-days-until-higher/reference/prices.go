// Package prices computes, for every trading day, the wait until a higher
// closing price for the pricing dashboard.
package prices

// DaysUntilHigher keeps a stack of days whose answer is still unknown; their
// prices decrease from bottom to top. Each new price pops every cheaper day
// on the stack and resolves it, so every day is pushed and popped once.
func DaysUntilHigher(prices []int) []int {
	out := make([]int, len(prices))
	stack := make([]int, 0, 64)
	for i, p := range prices {
		for len(stack) > 0 && prices[stack[len(stack)-1]] < p {
			j := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			out[j] = i - j
		}
		stack = append(stack, i)
	}
	return out
}
