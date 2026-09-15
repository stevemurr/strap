package occupancy

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name string
		grid []string
		want int
	}{
		{"readme", []string{"##..", "#...", "..#.", "...#"}, 3},
		{"diagonals do not connect", []string{"#.#", ".#.", "#.#"}, 5},
		{"solid", []string{"###", "###"}, 1},
		{"single row", []string{"#.#.#"}, 3},
		{"all empty", []string{"...", "..."}, 0},
		{"empty grid", nil, 0},
		{"empty slice", []string{}, 0},
		{"empty rows", []string{"", ""}, 0},
		{"single occupied", []string{"#"}, 1},
		{"single empty", []string{"."}, 0},
		{"single column", []string{"#", ".", "#", "#"}, 2},
		{"ring", []string{"###", "#.#", "###"}, 1},
		{"ring with centre", []string{"###", "#.#", "###", "...", ".#."}, 2},
		{"classic translated", []string{"##...", "##...", "..#..", "...##"}, 3},
		{"u shape", []string{"#.#", "#.#", "###"}, 1},
		{"stripes", []string{"#.#.#.", "#.#.#.", "#.#.#."}, 3},
	}
	for _, c := range cases {
		if got := CountClusters(c.grid); got != c.want {
			t.Errorf("%s: CountClusters(%q) = %d, want %d", c.name, c.grid, got, c.want)
		}
	}
}

// hiddenBruteClusters relabels every occupied cell with the smallest label among
// its neighbours until nothing changes, then counts the distinct labels.
func hiddenBruteClusters(grid []string) int {
	rows := len(grid)
	if rows == 0 || len(grid[0]) == 0 {
		return 0
	}
	cols := len(grid[0])
	label := make([][]int, rows)
	for r := range label {
		label[r] = make([]int, cols)
		for c := range label[r] {
			label[r][c] = -1
			if grid[r][c] == '#' {
				label[r][c] = r*cols + c
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for r := 0; r < rows; r++ {
			for c := 0; c < cols; c++ {
				if label[r][c] < 0 {
					continue
				}
				for _, d := range [][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
					nr, nc := r+d[0], c+d[1]
					if nr < 0 || nr >= rows || nc < 0 || nc >= cols || label[nr][nc] < 0 {
						continue
					}
					if label[nr][nc] < label[r][c] {
						label[r][c] = label[nr][nc]
						changed = true
					}
				}
			}
		}
	}
	distinct := make(map[int]bool)
	for r := range label {
		for _, l := range label[r] {
			if l >= 0 {
				distinct[l] = true
			}
		}
	}
	return len(distinct)
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(200))
	for round := 0; round < 500; round++ {
		rows, cols := rng.Intn(8), rng.Intn(8)
		density := []int{20, 50, 80}[round%3]
		grid := make([]string, rows)
		for r := range grid {
			b := make([]byte, cols)
			for c := range b {
				b[c] = '.'
				if rng.Intn(100) < density {
					b[c] = '#'
				}
			}
			grid[r] = string(b)
		}
		if got, want := CountClusters(grid), hiddenBruteClusters(grid); got != want {
			t.Fatalf("round %d: CountClusters(%q) = %d, want %d", round, grid, got, want)
		}
	}
}

func TestHiddenLargeGrid(t *testing.T) {
	const size = 1000
	solid := strings.Repeat("#", size)
	empty := strings.Repeat(".", size)
	even := strings.Repeat("#.", size/2)
	odd := strings.Repeat(".#", size/2)

	full := make([]string, size)
	blank := make([]string, size)
	checker := make([]string, size)
	rowStripes := make([]string, size)
	snake := make([]string, size)
	for r := 0; r < size; r++ {
		full[r] = solid
		blank[r] = empty
		checker[r] = even
		rowStripes[r] = solid
		snake[r] = solid
		if r%2 == 1 {
			checker[r] = odd
			rowStripes[r] = empty
			// Odd rows carry a single occupied cell joining the full rows above
			// and below, alternating sides so the cluster is one long path.
			b := []byte(empty)
			if (r/2)%2 == 0 {
				b[0] = '#'
			} else {
				b[size-1] = '#'
			}
			snake[r] = string(b)
		}
	}

	for _, shape := range []struct {
		name string
		grid []string
		want int
	}{
		{"fully occupied", full, 1},
		{"empty", blank, 0},
		{"checkerboard", checker, size * size / 2},
		{"row stripes", rowStripes, size / 2},
		{"snake", snake, 1},
	} {
		done := make(chan int, 1)
		go func() { done <- CountClusters(shape.grid) }()
		select {
		case got := <-done:
			if got != shape.want {
				t.Fatalf("%s: got %d clusters, want %d", shape.name, got, shape.want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: CountClusters took longer than 10s on a %dx%d grid", shape.name, size, size)
		}
	}
}
