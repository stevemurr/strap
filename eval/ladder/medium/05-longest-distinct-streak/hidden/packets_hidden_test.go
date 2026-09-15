package packets

import (
	"math/rand"
	"slices"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name string
		ids  []int
		want int
	}{
		{"readme", []int{3, 1, 4, 1, 5, 9, 2, 6}, 6},
		{"all same", []int{7, 7, 7}, 1},
		{"period three", []int{1, 2, 3, 1, 2, 3}, 3},
		{"abba", []int{1, 2, 2, 1}, 2},
		{"negative", []int{-1, 0, -1, 2, 3}, 4},
		{"empty", nil, 0},
		{"empty slice", []int{}, 0},
		{"single", []int{42}, 1},
		{"all distinct", []int{1, 2, 3, 4, 5}, 5},
		{"repeat at end", []int{1, 2, 3, 4, 1}, 4},
		{"repeat at start", []int{5, 5, 1, 2, 3}, 4},
		{"window must not move backwards", []int{1, 2, 3, 2, 1, 4, 5}, 5},
		{"zeros and negatives", []int{0, -0, 0}, 1},
		{"long gap between repeats", []int{9, 1, 2, 3, 9, 4, 5, 6, 7}, 8},
	}
	for _, c := range cases {
		if got := LongestDistinctStreak(c.ids); got != c.want {
			t.Errorf("%s: LongestDistinctStreak(%v) = %d, want %d", c.name, c.ids, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	ids := []int{3, 1, 4, 1, 5, 9, 2, 6, 5, 3}
	before := slices.Clone(ids)
	LongestDistinctStreak(ids)
	if !slices.Equal(ids, before) {
		t.Fatalf("input modified: %v", ids)
	}
}

// hiddenBruteStreak checks every contiguous run for duplicates.
func hiddenBruteStreak(ids []int) int {
	best := 0
	for i := range ids {
		for j := i + 1; j <= len(ids); j++ {
			seen := make(map[int]bool)
			distinct := true
			for _, id := range ids[i:j] {
				if seen[id] {
					distinct = false
					break
				}
				seen[id] = true
			}
			if distinct && j-i > best {
				best = j - i
			}
		}
	}
	return best
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for round := 0; round < 500; round++ {
		n := rng.Intn(40)
		spread := []int{2, 5, 12, 1000}[round%4]
		ids := make([]int, n)
		for i := range ids {
			ids[i] = rng.Intn(spread) - spread/2
		}
		if got, want := LongestDistinctStreak(ids), hiddenBruteStreak(ids); got != want {
			t.Fatalf("round %d: LongestDistinctStreak(%v) = %d, want %d", round, ids, got, want)
		}
	}
}

func TestHiddenLargeInput(t *testing.T) {
	const n = 1_000_000
	shapes := []struct {
		name string
		id   func(i int) int
		want int
	}{
		{"all distinct", func(i int) int { return i }, n},
		{"period 250000", func(i int) int { return i % 250_000 }, 250_000},
		{"negative period 400000", func(i int) int { return i%400_000 - 200_000 }, 400_000},
		{"growing periods", func(i int) int { return i % (1 + i/1000) }, 1000},
		{"constant", func(int) int { return -1 }, 1},
	}
	for _, shape := range shapes {
		ids := make([]int, n)
		for i := range ids {
			ids[i] = shape.id(i)
		}
		done := make(chan int, 1)
		go func() { done <- LongestDistinctStreak(ids) }()
		select {
		case got := <-done:
			if got != shape.want {
				t.Fatalf("%s: got %d, want %d", shape.name, got, shape.want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: LongestDistinctStreak took longer than 10s on %d ids", shape.name, n)
		}
	}
}
