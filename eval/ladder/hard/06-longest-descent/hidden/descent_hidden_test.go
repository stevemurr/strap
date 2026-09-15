package trails

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name      string
		elevation [][]int
		want      int
	}{
		{"readme first", [][]int{{9, 9, 4}, {6, 6, 8}, {2, 1, 1}}, 4},
		{"readme second", [][]int{{3, 4, 5}, {3, 2, 6}, {2, 2, 1}}, 4},
		{"loop", [][]int{{1, 2}, {4, 3}}, 4},
		{"all equal", [][]int{{7, 7}, {7, 7}}, 1},
		{"single", [][]int{{1}}, 1},
		{"empty", nil, 0},
		{"no rows", [][]int{}, 0},
		{"empty rows", [][]int{{}, {}}, 0},
		{"single row", [][]int{{5, 4, 3, 2, 1}}, 5},
		{"single column", [][]int{{1}, {2}, {3}}, 3},
		{"negatives", [][]int{{-1, -2}, {-4, -3}}, 4},
		{"no diagonals", [][]int{{2, 0}, {0, 1}}, 2},
		{"plateau breaks trail", [][]int{{3, 2, 2, 1}}, 2},
		{"tall then flat", [][]int{{5, 1, 1}, {1, 1, 1}}, 2},
		{"snake", [][]int{{1, 2, 3}, {6, 5, 4}, {7, 8, 9}}, 9},
		{"ridge", [][]int{{1, 5, 1}, {2, 6, 2}, {3, 7, 3}}, 4},
	}
	for _, c := range cases {
		if got := LongestDescent(c.elevation); got != c.want {
			t.Errorf("%s: LongestDescent(%v) = %d, want %d", c.name, c.elevation, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	elevation := [][]int{{9, 9, 4}, {6, 6, 8}, {2, 1, 1}}
	before := [][]int{{9, 9, 4}, {6, 6, 8}, {2, 1, 1}}
	LongestDescent(elevation)
	if !reflect.DeepEqual(elevation, before) {
		t.Fatalf("input modified: %v", elevation)
	}
}

// brute enumerates every trail from every cell without memoization.
func brute(g [][]int) int {
	rows := len(g)
	if rows == 0 || len(g[0]) == 0 {
		return 0
	}
	cols := len(g[0])
	var walk func(r, c int) int
	walk = func(r, c int) int {
		longest := 1
		for _, d := range [][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
			nr, nc := r+d[0], c+d[1]
			if nr >= 0 && nr < rows && nc >= 0 && nc < cols && g[nr][nc] < g[r][c] {
				if l := 1 + walk(nr, nc); l > longest {
					longest = l
				}
			}
		}
		return longest
	}
	best := 0
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			if l := walk(r, c); l > best {
				best = l
			}
		}
	}
	return best
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(329))
	for round := 0; round < 300; round++ {
		rows, cols := rng.Intn(6)+1, rng.Intn(6)+1
		spread := []int{3, 6, 50}[round%3]
		g := make([][]int, rows)
		for r := range g {
			g[r] = make([]int, cols)
			for c := range g[r] {
				g[r][c] = rng.Intn(spread) - spread/2
			}
		}
		if got, want := LongestDescent(g), brute(g); got != want {
			t.Fatalf("round %d: LongestDescent(%v) = %d, want %d", round, g, got, want)
		}
	}
}

func diagonal(rows, cols int) [][]int {
	g := make([][]int, rows)
	for r := range g {
		g[r] = make([]int, cols)
		for c := range g[r] {
			g[r][c] = r + c
		}
	}
	return g
}

func spiral(rows, cols int) [][]int {
	g := make([][]int, rows)
	for r := range g {
		g[r] = make([]int, cols)
	}
	top, bottom, left, right := 0, rows-1, 0, cols-1
	v := 0
	for top <= bottom && left <= right {
		for c := left; c <= right; c++ {
			g[top][c] = v
			v++
		}
		top++
		for r := top; r <= bottom; r++ {
			g[r][right] = v
			v++
		}
		right--
		if top <= bottom {
			for c := right; c >= left; c-- {
				g[bottom][c] = v
				v++
			}
			bottom--
		}
		if left <= right {
			for r := bottom; r >= top; r-- {
				g[r][left] = v
				v++
			}
			left++
		}
	}
	return g
}

func TestHiddenLargeGrid(t *testing.T) {
	const size = 500
	shapes := []struct {
		name string
		grid [][]int
		want int
	}{
		{"diagonal", diagonal(size, size), 2*size - 1},
		{"spiral", spiral(size, size), size * size},
	}
	for _, shape := range shapes {
		done := make(chan int, 1)
		go func() { done <- LongestDescent(shape.grid) }()
		select {
		case got := <-done:
			if got != shape.want {
				t.Fatalf("%s: LongestDescent = %d, want %d", shape.name, got, shape.want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: LongestDescent took longer than 10s on a %d x %d grid", shape.name, size, size)
		}
	}
}
