// Package tolls plans the cheapest courier route across a district grid.
package tolls

// CheapestRoute keeps one row of best costs: best[c] is the cheapest way to
// reach column c of the row being processed. Each cell is its own toll plus
// the cheaper of arriving from above (the old best[c]) or from the left
// (the new best[c-1]).
func CheapestRoute(toll [][]int) int {
	if len(toll) == 0 || len(toll[0]) == 0 {
		return 0
	}
	cols := len(toll[0])
	best := make([]int, cols)
	best[0] = toll[0][0]
	for c := 1; c < cols; c++ {
		best[c] = best[c-1] + toll[0][c]
	}
	for r := 1; r < len(toll); r++ {
		row := toll[r]
		best[0] += row[0]
		for c := 1; c < cols; c++ {
			from := best[c]
			if best[c-1] < from {
				from = best[c-1]
			}
			best[c] = from + row[c]
		}
	}
	return best[cols-1]
}
