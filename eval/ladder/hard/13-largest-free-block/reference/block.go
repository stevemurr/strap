// Package seating finds free rectangular blocks in a venue seating grid.
package seating

// LargestFreeBlock walks the rows top to bottom keeping, for every column, the
// number of consecutive free seats ending at the current row. Each row is then
// a histogram, and the largest rectangle under it is found with a monotonic
// stack, so the whole grid costs O(rows*cols).
func LargestFreeBlock(grid []string) int {
	if len(grid) == 0 || len(grid[0]) == 0 {
		return 0
	}
	cols := len(grid[0])
	heights := make([]int, cols)
	stack := make([]int, 0, cols+1)
	best := 0
	for _, row := range grid {
		for c := 0; c < cols; c++ {
			if row[c] == '.' {
				heights[c]++
			} else {
				heights[c] = 0
			}
		}
		stack = stack[:0]
		for c := 0; c <= cols; c++ {
			h := 0
			if c < cols {
				h = heights[c]
			}
			for len(stack) > 0 && heights[stack[len(stack)-1]] >= h {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				left := -1
				if len(stack) > 0 {
					left = stack[len(stack)-1]
				}
				if area := heights[top] * (c - left - 1); area > best {
					best = area
				}
			}
			stack = append(stack, c)
		}
	}
	return best
}
