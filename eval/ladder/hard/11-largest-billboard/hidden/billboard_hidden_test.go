package skyline

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name    string
		heights []int
		want    int
	}{
		{"readme", []int{2, 1, 5, 6, 2, 3}, 10},
		{"two", []int{2, 4}, 4},
		{"flat", []int{5, 5, 5}, 15},
		{"gap", []int{3, 0, 3}, 3},
		{"single", []int{7}, 7},
		{"empty", nil, 0},
		{"empty slice", []int{}, 0},
		{"zero", []int{0}, 0},
		{"all zero", []int{0, 0, 0}, 0},
		{"increasing", []int{1, 2, 3, 4, 5}, 9},
		{"decreasing", []int{5, 4, 3, 2, 1}, 9},
		{"valley", []int{6, 2, 5, 4, 5, 1, 6}, 12},
		{"peak in middle", []int{1, 1, 9, 1, 1}, 9},
		{"wide low beats tall narrow", []int{2, 2, 2, 2, 7}, 10},
		{"zeros split", []int{4, 4, 0, 4, 4, 4}, 12},
	}
	for _, c := range cases {
		if got := LargestBillboard(c.heights); got != c.want {
			t.Errorf("%s: LargestBillboard(%v) = %d, want %d", c.name, c.heights, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	heights := []int{3, 1, 4, 1, 5, 9, 2, 6}
	before := append([]int(nil), heights...)
	LargestBillboard(heights)
	if !reflect.DeepEqual(heights, before) {
		t.Fatalf("input modified: %v", heights)
	}
}

func brute(heights []int) int {
	best := 0
	for l := range heights {
		m := heights[l]
		for r := l; r < len(heights); r++ {
			if heights[r] < m {
				m = heights[r]
			}
			if area := m * (r - l + 1); area > best {
				best = area
			}
		}
	}
	return best
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(84))
	for round := 0; round < 400; round++ {
		n := rng.Intn(50) + 1
		heights := make([]int, n)
		spread := []int{2, 5, 20, 1000}[round%4]
		for i := range heights {
			heights[i] = rng.Intn(spread)
		}
		got, want := LargestBillboard(heights), brute(heights)
		if got != want {
			t.Fatalf("round %d: LargestBillboard(%v) = %d, want %d", round, heights, got, want)
		}
	}
}

func TestHiddenLargeSkyline(t *testing.T) {
	const n = 1_000_000
	increasing := make([]int, n)
	decreasing := make([]int, n)
	flat := make([]int, n)
	for i := range increasing {
		increasing[i] = i + 1
		decreasing[i] = n - i
		flat[i] = 7
	}
	// The best rectangle under a staircase of n steps sits at the middle step.
	const staircase = 500_000 * 500_001
	for _, shape := range []struct {
		name string
		data []int
		want int
	}{
		{"increasing", increasing, staircase},
		{"decreasing", decreasing, staircase},
		{"flat", flat, 7 * n},
	} {
		done := make(chan int, 1)
		go func() { done <- LargestBillboard(shape.data) }()
		select {
		case got := <-done:
			if got != shape.want {
				t.Fatalf("%s: got %d, want %d", shape.name, got, shape.want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: LargestBillboard took longer than 10s on %d slots", shape.name, n)
		}
	}
}
