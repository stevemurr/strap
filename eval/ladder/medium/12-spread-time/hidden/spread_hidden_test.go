package outbreak

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
		{"readme spread", []string{"ISS", "SS.", ".SS"}, 4},
		{"readme unreachable", []string{"ISS", ".SS", "S.S"}, -1},
		{"readme nothing susceptible", []string{".I"}, 0},
		{"two sources", []string{"ISSSI"}, 2},
		{"no source", []string{"SSS"}, -1},
		{"empty", nil, 0},
		{"zero-length rows", []string{"", ""}, 0},
		{"single infected", []string{"I"}, 0},
		{"single susceptible", []string{"S"}, -1},
		{"single empty", []string{"."}, 0},
		{"all infected", []string{"II", "II"}, 0},
		{"all empty", []string{"..", ".."}, 0},
		{"diagonal does not spread", []string{"I.", ".S"}, -1},
		{"walled off", []string{"I.S", "...", "S.S"}, -1},
		{"ring", []string{"SSS", "SIS", "SSS"}, 2},
		{"corridor", []string{"ISSSSSSS"}, 7},
		{"column", []string{"S", "S", "I", "S"}, 2},
		{"two components both seeded", []string{"IS.SI"}, 1},
		{"far corner", []string{"I...", "SSSS", "...S"}, 5},
	}
	for _, c := range cases {
		if got := SpreadTime(c.grid); got != c.want {
			t.Errorf("%s: SpreadTime(%q) = %d, want %d", c.name, c.grid, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	grid := []string{"ISS", "SS.", ".SS"}
	before := append([]string(nil), grid...)
	SpreadTime(grid)
	for i := range grid {
		if grid[i] != before[i] {
			t.Fatalf("input modified: %q", grid)
		}
	}
}

// simulate is the minute-by-minute brute force.
func simulate(grid []string) int {
	rows := make([][]byte, len(grid))
	for r := range grid {
		rows[r] = []byte(grid[r])
	}
	minutes := 0
	for {
		var fresh [][2]int
		for r := range rows {
			for c := range rows[r] {
				if rows[r][c] != 'I' {
					continue
				}
				for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
					nr, nc := r+d[0], c+d[1]
					if nr >= 0 && nr < len(rows) && nc >= 0 && nc < len(rows[nr]) && rows[nr][nc] == 'S' {
						fresh = append(fresh, [2]int{nr, nc})
					}
				}
			}
		}
		if len(fresh) == 0 {
			break
		}
		for _, p := range fresh {
			rows[p[0]][p[1]] = 'I'
		}
		minutes++
	}
	for r := range rows {
		if strings.IndexByte(string(rows[r]), 'S') >= 0 {
			return -1
		}
	}
	return minutes
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(994))
	for round := 0; round < 500; round++ {
		rows, cols := rng.Intn(7)+1, rng.Intn(7)+1
		grid := make([]string, rows)
		for r := range grid {
			row := make([]byte, cols)
			for c := range row {
				switch v := rng.Intn(10); {
				case v == 0 && round%5 != 0:
					row[c] = 'I'
				case v < 7:
					row[c] = 'S'
				default:
					row[c] = '.'
				}
			}
			grid[r] = string(row)
		}
		got, want := SpreadTime(grid), simulate(grid)
		if got != want {
			t.Fatalf("round %d: SpreadTime(%q) = %d, want %d", round, grid, got, want)
		}
	}
}

// serpentine builds an n x n grid whose susceptible cells form one winding
// corridor: even rows are corridors, odd rows are walls with a single
// connector cell at alternating ends. The infection starts at (0, 0).
func serpentine(n int) []string {
	grid := make([]string, n)
	for r := 0; r < n; r++ {
		row := make([]byte, n)
		if r%2 == 0 {
			for c := range row {
				row[c] = 'S'
			}
		} else {
			for c := range row {
				row[c] = '.'
			}
			if ((r-1)/2)%2 == 0 {
				row[n-1] = 'S'
			} else {
				row[0] = 'S'
			}
		}
		grid[r] = string(row)
	}
	grid[0] = "I" + grid[0][1:]
	return grid
}

func TestHiddenLargeGrid(t *testing.T) {
	const n = 1000
	run := func(name string, grid []string) int {
		done := make(chan int, 1)
		go func() { done <- SpreadTime(grid) }()
		select {
		case got := <-done:
			return got
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: SpreadTime took longer than 10s on a %dx%d grid", name, n, n)
			return 0
		}
	}

	// Corridor of 500 rows of n-1 steps each plus the connectors between
	// them: the end of corridor row 2m is 999 + 1001*m steps away, so the
	// last row's connector at (999, 0) is 999 + 1001*499 + 1 = 500499 away.
	if got, want := run("serpentine", serpentine(n)), 500499; got != want {
		t.Fatalf("serpentine: got %d, want %d", got, want)
	}

	// Cutting the last corridor row leaves its left part unreachable.
	cut := serpentine(n)
	row := []byte(cut[n-2])
	row[n/2] = '.'
	cut[n-2] = string(row)
	if got := run("cut serpentine", cut); got != -1 {
		t.Fatalf("cut serpentine: got %d, want -1", got)
	}

	// Open floor: the far corner is (n-1)+(n-1) steps away.
	open := make([]string, n)
	full := strings.Repeat("S", n)
	for r := range open {
		open[r] = full
	}
	open[0] = "I" + full[1:]
	if got, want := run("open", open), 2*(n-1); got != want {
		t.Fatalf("open: got %d, want %d", got, want)
	}
}
