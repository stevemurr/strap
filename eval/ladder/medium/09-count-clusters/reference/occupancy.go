// Package occupancy analyses floor-plan grids of occupied and empty desks.
package occupancy

// CountClusters flood-fills from every unvisited occupied cell using an
// explicit stack, so a million-cell cluster costs a million steps of heap
// memory rather than a million stack frames. One visited bitmap is shared by
// all clusters, so the whole grid is touched a constant number of times.
func CountClusters(grid []string) int {
	rows := len(grid)
	if rows == 0 {
		return 0
	}
	cols := len(grid[0])
	if cols == 0 {
		return 0
	}
	visited := make([]bool, rows*cols)
	stack := make([]int, 0, 1024)
	push := func(r, c int) {
		if r < 0 || r >= rows || c < 0 || c >= cols || grid[r][c] != '#' || visited[r*cols+c] {
			return
		}
		visited[r*cols+c] = true
		stack = append(stack, r*cols+c)
	}
	clusters := 0
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			if grid[r][c] != '#' || visited[r*cols+c] {
				continue
			}
			clusters++
			push(r, c)
			for len(stack) > 0 {
				cell := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				cr, cc := cell/cols, cell%cols
				push(cr-1, cc)
				push(cr+1, cc)
				push(cr, cc-1)
				push(cr, cc+1)
			}
		}
	}
	return clusters
}
