// Package trading evaluates price histories for the backtesting notebook.
package trading

// BestTrade scans once, tracking the first day with the lowest price so far.
// Every day is a candidate sell against that low; recording only strictly
// better profits keeps the smallest buy day and then the smallest sell day.
func BestTrade(prices []int) (buy, sell, profit int) {
	low := 0
	for day := 1; day < len(prices); day++ {
		if p := prices[day] - prices[low]; p > profit {
			buy, sell, profit = low, day, p
		}
		if prices[day] < prices[low] {
			low = day
		}
	}
	return buy, sell, profit
}
