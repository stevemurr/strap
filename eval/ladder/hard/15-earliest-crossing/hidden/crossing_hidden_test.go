package floodmap

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name string
		grid [][]int
		want int
	}{
		{"readme", [][]int{{0, 2}, {1, 3}}, 3},
		{"spiral", [][]int{{0, 1, 2, 3, 4}, {24, 23, 22, 21, 5}, {12, 13, 14, 15, 16}, {11, 17, 18, 19, 20}, {10, 9, 8, 7, 6}}, 16},
		{"around the wall", [][]int{{0, 9, 0}, {0, 9, 0}, {0, 0, 0}}, 0},
		{"start is highest", [][]int{{3, 0}, {0, 0}}, 3},
		{"end is highest", [][]int{{0, 0}, {0, 4}}, 4},
		{"single", [][]int{{5}}, 5},
		{"nil", nil, 0},
		{"no rows", [][]int{}, 0},
		{"empty row", [][]int{{}}, 0},
		{"one row", [][]int{{1, 8, 2, 4}}, 8},
		{"one column", [][]int{{1}, {0}, {3}, {2}}, 3},
		{"pick the lower detour", [][]int{{0, 7, 0}, {2, 9, 1}, {0, 5, 0}}, 5},
		{"flat", [][]int{{2, 2}, {2, 2}}, 2},
	}
	for _, c := range cases {
		if got := EarliestCrossing(c.grid); got != c.want {
			t.Errorf("%s: EarliestCrossing(%v) = %d, want %d", c.name, c.grid, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	grid := [][]int{{0, 5, 1}, {3, 4, 2}, {6, 0, 1}}
	before := [][]int{{0, 5, 1}, {3, 4, 2}, {6, 0, 1}}
	EarliestCrossing(grid)
	if !reflect.DeepEqual(grid, before) {
		t.Fatalf("input modified: %v", grid)
	}
}

// hiddenReachable reports whether the corners connect when only cells with
// elevation <= level may be used.
func hiddenReachable(grid [][]int, level int) bool {
	rows, cols := len(grid), len(grid[0])
	if grid[0][0] > level {
		return false
	}
	seen := make([][]bool, rows)
	for r := range seen {
		seen[r] = make([]bool, cols)
	}
	queue := [][2]int{{0, 0}}
	seen[0][0] = true
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur[0] == rows-1 && cur[1] == cols-1 {
			return true
		}
		for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			r, c := cur[0]+d[0], cur[1]+d[1]
			if r < 0 || r >= rows || c < 0 || c >= cols || seen[r][c] || grid[r][c] > level {
				continue
			}
			seen[r][c] = true
			queue = append(queue, [2]int{r, c})
		}
	}
	return false
}

func hiddenBrute(grid [][]int) int {
	if len(grid) == 0 || len(grid[0]) == 0 {
		return 0
	}
	for level := 0; ; level++ {
		if hiddenReachable(grid, level) {
			return level
		}
	}
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(778))
	for round := 0; round < 300; round++ {
		rows, cols := rng.Intn(6)+1, rng.Intn(6)+1
		spread := []int{3, 10, 40}[round%3]
		grid := make([][]int, rows)
		for r := range grid {
			grid[r] = make([]int, cols)
			for c := range grid[r] {
				grid[r][c] = rng.Intn(spread)
			}
		}
		got, want := EarliestCrossing(grid), hiddenBrute(grid)
		if got != want {
			t.Fatalf("round %d: EarliestCrossing(%v) = %d, want %d", round, grid, got, want)
		}
	}
}

func TestHiddenLargeDistrict(t *testing.T) {
	const size = 500
	rng := rand.New(rand.NewSource(11))
	// A wall of high cells down the middle column with a single lower gap:
	// every route must cross the wall, so the gap's elevation is the answer.
	walled := make([][]int, size)
	ramp := make([][]int, size)
	for r := range walled {
		walled[r] = make([]int, size)
		ramp[r] = make([]int, size)
		for c := range walled[r] {
			switch {
			case c != size/2:
				walled[r][c] = rng.Intn(100_000)
			case r == 123:
				walled[r][c] = 150_000
			default:
				walled[r][c] = 200_000 + rng.Intn(50_000)
			}
			ramp[r][c] = r + c
		}
	}
	for _, shape := range []struct {
		name string
		grid [][]int
		want int
	}{
		{"walled", walled, 150_000},
		{"ramp", ramp, 2*size - 2},
	} {
		done := make(chan int, 1)
		go func() { done <- EarliestCrossing(shape.grid) }()
		select {
		case got := <-done:
			if got != shape.want {
				t.Fatalf("%s: got %d, want %d", shape.name, got, shape.want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: EarliestCrossing took longer than 10s on a %dx%d grid", shape.name, size, size)
		}
	}
}
