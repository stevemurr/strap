// Package trading scores simple buy-and-sell strategies for the backtester.
package trading

import "math"

// MaxProfit runs the classic O(n*k) dynamic programme: hold[t] is the best
// balance after buying the share of trade t, done[t] the best balance after
// completing t trades. When k is at least half the number of days the limit
// no longer binds, since every trade needs two distinct days, and the answer
// is simply the sum of all positive day-to-day price moves.
func MaxProfit(prices []int, k int) int {
	n := len(prices)
	if k < 1 || n < 2 {
		return 0
	}
	if k >= n/2 {
		total := 0
		for i := 1; i < n; i++ {
			if d := prices[i] - prices[i-1]; d > 0 {
				total += d
			}
		}
		return total
	}
	const never = math.MinInt / 2
	hold := make([]int, k+1)
	done := make([]int, k+1)
	for t := range hold {
		hold[t] = never
	}
	for _, p := range prices {
		for t := k; t >= 1; t-- {
			if done[t-1]-p > hold[t] {
				hold[t] = done[t-1] - p
			}
			if hold[t]+p > done[t] {
				done[t] = hold[t] + p
			}
		}
	}
	return done[k]
}
