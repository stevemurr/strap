package seating

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
		{"readme", []string{".#.##", ".#...", ".....", ".##.#"}, 6},
		{"square", []string{"..", ".."}, 4},
		{"ring", []string{"...", ".#.", "..."}, 3},
		{"diagonal", []string{".#", "#."}, 1},
		{"taken", []string{"#"}, 0},
		{"free", []string{"."}, 1},
		{"nil", nil, 0},
		{"no rows", []string{}, 0},
		{"empty rows", []string{"", ""}, 0},
		{"all taken", []string{"##", "##"}, 0},
		{"single row", []string{"..#...."}, 4},
		{"single column", []string{".", ".", "#", ".", ".", "."}, 3},
		{"tall beats wide", []string{"....", "..##", "..##", "..##", "..##"}, 10},
		{"wide beats tall", []string{".....", ".....", "#####", "...##"}, 10},
		{"checker", []string{".#.#", "#.#.", ".#.#"}, 1},
	}
	for _, c := range cases {
		if got := LargestFreeBlock(c.grid); got != c.want {
			t.Errorf("%s: LargestFreeBlock(%q) = %d, want %d", c.name, c.grid, got, c.want)
		}
	}
}

func hiddenBrute(grid []string) int {
	best := 0
	if len(grid) == 0 {
		return 0
	}
	rows, cols := len(grid), len(grid[0])
	for r1 := 0; r1 < rows; r1++ {
		for c1 := 0; c1 < cols; c1++ {
			for r2 := r1; r2 < rows; r2++ {
				for c2 := c1; c2 < cols; c2++ {
					free := true
					for r := r1; r <= r2 && free; r++ {
						for c := c1; c <= c2; c++ {
							if grid[r][c] != '.' {
								free = false
								break
							}
						}
					}
					if free {
						if area := (r2 - r1 + 1) * (c2 - c1 + 1); area > best {
							best = area
						}
					}
				}
			}
		}
	}
	return best
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(85))
	for round := 0; round < 300; round++ {
		rows, cols := rng.Intn(7)+1, rng.Intn(7)+1
		takenPercent := []int{10, 30, 50, 80}[round%4]
		grid := make([]string, rows)
		for r := range grid {
			var b strings.Builder
			for c := 0; c < cols; c++ {
				if rng.Intn(100) < takenPercent {
					b.WriteByte('#')
				} else {
					b.WriteByte('.')
				}
			}
			grid[r] = b.String()
		}
		got, want := LargestFreeBlock(grid), hiddenBrute(grid)
		if got != want {
			t.Fatalf("round %d: LargestFreeBlock(%q) = %d, want %d", round, grid, got, want)
		}
	}
}

func TestHiddenLargeVenue(t *testing.T) {
	const size = 1000
	free := strings.Repeat(".", size)
	allFree := make([]string, size)
	oneTaken := make([]string, size)
	for r := range allFree {
		allFree[r] = free
		oneTaken[r] = free
	}
	oneTaken[size/2] = free[:size/2] + "#" + free[size/2+1:]
	for _, shape := range []struct {
		name string
		grid []string
		want int
	}{
		{"all free", allFree, size * size},
		{"one taken seat in the middle", oneTaken, size * size / 2},
	} {
		done := make(chan int, 1)
		go func() { done <- LargestFreeBlock(shape.grid) }()
		select {
		case got := <-done:
			if got != shape.want {
				t.Fatalf("%s: got %d, want %d", shape.name, got, shape.want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: LargestFreeBlock took longer than 10s on a %dx%d grid", shape.name, size, size)
		}
	}
}
