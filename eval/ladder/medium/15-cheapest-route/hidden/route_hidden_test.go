package tolls

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name string
		toll [][]int
		want int
	}{
		{"readme classic", [][]int{{1, 3, 1}, {1, 5, 1}, {4, 2, 1}}, 7},
		{"readme two rows", [][]int{{1, 2, 3}, {4, 5, 6}}, 12},
		{"readme single", [][]int{{5}}, 5},
		{"readme one row", [][]int{{1, 2, 3}}, 6},
		{"readme zeros", [][]int{{0, 0}, {0, 0}}, 0},
		{"readme empty", nil, 0},
		{"empty rows", [][]int{{}, {}}, 0},
		{"one column", [][]int{{4}, {2}, {9}}, 15},
		{"cheap corridor", [][]int{{1, 9, 9}, {1, 9, 9}, {1, 1, 1}}, 5},
		{"expensive corner", [][]int{{1, 1}, {1, 100}}, 102},
		{"zero start", [][]int{{0, 5}, {5, 0}}, 5},
		{"square four", [][]int{{1, 2, 3, 4}, {2, 3, 4, 5}, {3, 4, 5, 6}, {4, 5, 6, 7}}, 28},
	}
	for _, c := range cases {
		if got := CheapestRoute(c.toll); got != c.want {
			t.Errorf("%s: CheapestRoute(%v) = %d, want %d", c.name, c.toll, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	toll := [][]int{{1, 3, 1}, {1, 5, 1}, {4, 2, 1}}
	before := make([][]int, len(toll))
	for r := range toll {
		before[r] = append([]int(nil), toll[r]...)
	}
	CheapestRoute(toll)
	if !reflect.DeepEqual(toll, before) {
		t.Fatalf("input modified: %v", toll)
	}
}

func brute(toll [][]int, r, c int) int {
	cost := toll[r][c]
	if r == len(toll)-1 && c == len(toll[0])-1 {
		return cost
	}
	best := -1
	if r+1 < len(toll) {
		best = brute(toll, r+1, c)
	}
	if c+1 < len(toll[0]) {
		if v := brute(toll, r, c+1); best < 0 || v < best {
			best = v
		}
	}
	return cost + best
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(64))
	for round := 0; round < 400; round++ {
		rows, cols := rng.Intn(6)+1, rng.Intn(6)+1
		spread := []int{2, 10, 1000}[round%3]
		toll := make([][]int, rows)
		for r := range toll {
			toll[r] = make([]int, cols)
			for c := range toll[r] {
				toll[r][c] = rng.Intn(spread)
			}
		}
		got, want := CheapestRoute(toll), brute(toll, 0, 0)
		if got != want {
			t.Fatalf("round %d: CheapestRoute(%v) = %d, want %d", round, toll, got, want)
		}
	}
}

func TestHiddenLargeGrid(t *testing.T) {
	const m, n = 2000, 2000
	run := func(name string, toll [][]int) int {
		done := make(chan int, 1)
		go func() { done <- CheapestRoute(toll) }()
		select {
		case got := <-done:
			return got
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: CheapestRoute took longer than 10s on a %dx%d grid", name, m, n)
			return 0
		}
	}
	fill := func(v int) [][]int {
		toll := make([][]int, m)
		for r := range toll {
			toll[r] = make([]int, n)
			for c := range toll[r] {
				toll[r][c] = v
			}
		}
		return toll
	}

	if got := run("ones", fill(1)); got != m+n-1 {
		t.Fatalf("ones: got %d, want %d", got, m+n-1)
	}

	// Plant one random monotone route of toll 1 through a grid of toll 3.
	// Any route that leaves it pays more, so the answer is m+n-1.
	rng := rand.New(rand.NewSource(640))
	planted := fill(3)
	r, c := 0, 0
	planted[0][0] = 1
	for r < m-1 || c < n-1 {
		if r == m-1 || (c < n-1 && rng.Intn(2) == 0) {
			c++
		} else {
			r++
		}
		planted[r][c] = 1
	}
	if got := run("planted", planted); got != m+n-1 {
		t.Fatalf("planted: got %d, want %d", got, m+n-1)
	}
	ones := 0
	for r := range planted {
		for _, v := range planted[r] {
			if v == 1 {
				ones++
			}
		}
	}
	if ones != m+n-1 {
		t.Fatalf("planted grid was modified: %d cells of toll 1, want %d", ones, m+n-1)
	}
}
