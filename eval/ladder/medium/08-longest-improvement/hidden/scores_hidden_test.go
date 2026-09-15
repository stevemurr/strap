package scores

import (
	"math/rand"
	"slices"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name   string
		scores []int
		want   int
	}{
		{"readme", []int{10, 9, 2, 5, 3, 7, 101, 18}, 4},
		{"repeats inside", []int{0, 1, 0, 3, 2, 3}, 4},
		{"all equal", []int{7, 7, 7}, 1},
		{"decreasing", []int{4, 3, 2, 1}, 1},
		{"negative", []int{-5, -3, -4, 0}, 3},
		{"empty", nil, 0},
		{"empty slice", []int{}, 0},
		{"single", []int{42}, 1},
		{"increasing", []int{1, 2, 3, 4, 5}, 5},
		{"equal pair", []int{2, 2}, 1},
		{"strict not weak", []int{1, 3, 3, 4}, 3},
		{"late long run", []int{9, 8, 7, 1, 2, 3}, 3},
		{"zigzag", []int{1, 5, 2, 6, 3, 7, 4, 8}, 5},
		{"large gaps", []int{-1000, 1000, -999, 999, 1001}, 4},
	}
	for _, c := range cases {
		if got := LongestImprovement(c.scores); got != c.want {
			t.Errorf("%s: LongestImprovement(%v) = %d, want %d", c.name, c.scores, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	scores := []int{10, 9, 2, 5, 3, 7, 101, 18}
	before := slices.Clone(scores)
	LongestImprovement(scores)
	if !slices.Equal(scores, before) {
		t.Fatalf("input modified: %v", scores)
	}
}

// hiddenBruteLongest is the quadratic dynamic programme over pairs of positions.
func hiddenBruteLongest(scores []int) int {
	best := 0
	ending := make([]int, len(scores))
	for i := range scores {
		ending[i] = 1
		for j := 0; j < i; j++ {
			if scores[j] < scores[i] && ending[j]+1 > ending[i] {
				ending[i] = ending[j] + 1
			}
		}
		best = max(best, ending[i])
	}
	return best
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(300))
	for round := 0; round < 500; round++ {
		n := rng.Intn(60)
		spread := []int{3, 10, 100, 100000}[round%4]
		scores := make([]int, n)
		for i := range scores {
			scores[i] = rng.Intn(spread) - spread/2
		}
		if got, want := LongestImprovement(scores), hiddenBruteLongest(scores); got != want {
			t.Fatalf("round %d: LongestImprovement(%v) = %d, want %d", round, scores, got, want)
		}
	}
}

func TestHiddenLargeInput(t *testing.T) {
	const n = 300_000
	shapes := []struct {
		name  string
		score func(i int) int
		want  int
	}{
		{"increasing", func(i int) int { return i }, n},
		{"decreasing", func(i int) int { return n - i }, 1},
		{"constant", func(int) int { return 5 }, 1},
		// Blocks of 1000 decreasing scores, each block above the previous: one
		// score per block.
		{"decreasing blocks", func(i int) int { return (i/1000)*1000 + 999 - i%1000 }, n / 1000},
		// Even positions climb, odd positions sink: the even positions form the
		// longest run.
		{"alternating", func(i int) int {
			if i%2 == 0 {
				return i
			}
			return -i
		}, n / 2},
	}
	for _, shape := range shapes {
		scores := make([]int, n)
		for i := range scores {
			scores[i] = shape.score(i)
		}
		done := make(chan int, 1)
		go func() { done <- LongestImprovement(scores) }()
		select {
		case got := <-done:
			if got != shape.want {
				t.Fatalf("%s: got %d, want %d", shape.name, got, shape.want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: LongestImprovement took longer than 10s on %d scores", shape.name, n)
		}
	}
}
