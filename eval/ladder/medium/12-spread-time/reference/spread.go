// Package outbreak simulates a worm spreading through the device rack.
package outbreak

// SpreadTime runs a breadth-first search seeded with every infected machine.
// Each BFS level is one minute; the search stops as soon as the last
// susceptible machine is infected, and any survivor means -1.
func SpreadTime(grid []string) int {
	rows := len(grid)
	if rows == 0 {
		return 0
	}
	cols := len(grid[0])
	if cols == 0 {
		return 0
	}
	infected := make([]bool, rows*cols)
	frontier := make([]int, 0, 1024)
	susceptible := 0
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			switch grid[r][c] {
			case 'I':
				infected[r*cols+c] = true
				frontier = append(frontier, r*cols+c)
			case 'S':
				susceptible++
			}
		}
	}
	next := make([]int, 0, 1024)
	minutes := 0
	for susceptible > 0 && len(frontier) > 0 {
		next = next[:0]
		for _, cell := range frontier {
			r, c := cell/cols, cell%cols
			if r > 0 {
				next = spread(grid, infected, next, r-1, c, cols, &susceptible)
			}
			if r+1 < rows {
				next = spread(grid, infected, next, r+1, c, cols, &susceptible)
			}
			if c > 0 {
				next = spread(grid, infected, next, r, c-1, cols, &susceptible)
			}
			if c+1 < cols {
				next = spread(grid, infected, next, r, c+1, cols, &susceptible)
			}
		}
		if len(next) == 0 {
			break
		}
		minutes++
		frontier, next = next, frontier
	}
	if susceptible > 0 {
		return -1
	}
	return minutes
}

func spread(grid []string, infected []bool, next []int, r, c, cols int, susceptible *int) []int {
	i := r*cols + c
	if grid[r][c] != 'S' || infected[i] {
		return next
	}
	infected[i] = true
	*susceptible--
	return append(next, i)
}
