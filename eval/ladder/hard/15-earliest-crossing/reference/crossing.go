// Package floodmap answers reachability questions about a flooded district.
package floodmap

import "container/heap"

// EarliestCrossing runs Dijkstra's algorithm where the cost of a route is the
// highest elevation on it. The first time the bottom-right cell is popped,
// its cost is the minimax elevation and therefore the earliest water level.
func EarliestCrossing(elevation [][]int) int {
	rows := len(elevation)
	if rows == 0 || len(elevation[0]) == 0 {
		return 0
	}
	cols := len(elevation[0])
	seen := make([]bool, rows*cols)
	pq := &frontier{{level: elevation[0][0]}}
	for pq.Len() > 0 {
		cur := heap.Pop(pq).(cell)
		id := cur.r*cols + cur.c
		if seen[id] {
			continue
		}
		seen[id] = true
		if cur.r == rows-1 && cur.c == cols-1 {
			return cur.level
		}
		for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			r, c := cur.r+d[0], cur.c+d[1]
			if r < 0 || r >= rows || c < 0 || c >= cols || seen[r*cols+c] {
				continue
			}
			heap.Push(pq, cell{r: r, c: c, level: max(cur.level, elevation[r][c])})
		}
	}
	return 0
}

type cell struct {
	r, c  int
	level int
}

type frontier []cell

func (f frontier) Len() int           { return len(f) }
func (f frontier) Less(i, j int) bool { return f[i].level < f[j].level }
func (f frontier) Swap(i, j int)      { f[i], f[j] = f[j], f[i] }
func (f *frontier) Push(x any)        { *f = append(*f, x.(cell)) }
func (f *frontier) Pop() any          { old := *f; x := old[len(old)-1]; *f = old[:len(old)-1]; return x }
