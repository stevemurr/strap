// Package trails finds long downhill runs across elevation grids for the
// trail-planning tool.
package trails

// LongestDescent memoizes, per cell, the length of the longest trail that
// starts there. A trail from a cell continues into any lower neighbour, so
// the answer for a cell is one more than the best answer among its lower
// neighbours; each cell is solved once and reused by every neighbour above.
func LongestDescent(elevation [][]int) int {
	rows := len(elevation)
	if rows == 0 || len(elevation[0]) == 0 {
		return 0
	}
	cols := len(elevation[0])
	best := make([]int, rows*cols)
	var from func(r, c int) int
	from = func(r, c int) int {
		if v := best[r*cols+c]; v != 0 {
			return v
		}
		longest := 1
		h := elevation[r][c]
		if r > 0 && elevation[r-1][c] < h {
			longest = max(longest, 1+from(r-1, c))
		}
		if r+1 < rows && elevation[r+1][c] < h {
			longest = max(longest, 1+from(r+1, c))
		}
		if c > 0 && elevation[r][c-1] < h {
			longest = max(longest, 1+from(r, c-1))
		}
		if c+1 < cols && elevation[r][c+1] < h {
			longest = max(longest, 1+from(r, c+1))
		}
		best[r*cols+c] = longest
		return longest
	}
	overall := 0
	for r := range rows {
		for c := range cols {
			overall = max(overall, from(r, c))
		}
	}
	return overall
}
