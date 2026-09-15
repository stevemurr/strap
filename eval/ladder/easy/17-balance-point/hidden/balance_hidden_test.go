package balance

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		weights []int
		want    int
	}{
		{[]int{1, 7, 3, 6, 5, 6}, 3},
		{[]int{1, 2, 3}, -1},
		{[]int{2, 1, -1}, 0},
		{[]int{0, 0, 0}, 0},
		{[]int{5}, 0},
		{nil, -1},
		{[]int{}, -1},
		{[]int{-7}, 0},
		{[]int{1, -1, 0}, 2},
		{[]int{2, 3, -1, 8, 4}, 3},
		{[]int{-1, -1, -1, 0, 1, 1}, 0},
		{[]int{1, 1}, -1},
		{[]int{1, 0, 1}, 1},
		{[]int{0, 1}, 1},
		{[]int{1, 0}, 0},
		{[]int{3, -3, 4, 0}, 2},
		{[]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, -1},
		{[]int{-1000000, 1000000, 0, 1000000, -1000000}, 2},
	}
	for _, c := range cases {
		if got := Point(c.weights); got != c.want {
			t.Errorf("Point(%v) = %d, want %d", c.weights, got, c.want)
		}
	}
}

func TestHiddenLeftmostWins(t *testing.T) {
	cases := []struct {
		weights []int
		want    int
	}{
		{[]int{0, 0, 0}, 0},
		{[]int{1, 2, -2, 2, 1}, 1},
		{[]int{5, -5, 5, -5, 5}, 0},
		{[]int{0, 0, 7, 0, 0}, 2},
		{[]int{2, 0, 0, 2}, 1},
	}
	for _, c := range cases {
		if got := Point(c.weights); got != c.want {
			t.Errorf("Point(%v) = %d, want %d", c.weights, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	weights := []int{4, -2, 0, 7, 1}
	before := append([]int(nil), weights...)
	Point(weights)
	if !reflect.DeepEqual(weights, before) {
		t.Fatalf("input modified: %v", weights)
	}
}

func hiddenBrute(weights []int) int {
	for i := range weights {
		left, right := 0, 0
		for _, w := range weights[:i] {
			left += w
		}
		for _, w := range weights[i+1:] {
			right += w
		}
		if left == right {
			return i
		}
	}
	return -1
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(724))
	for round := 0; round < 3000; round++ {
		n := rng.Intn(12)
		weights := make([]int, n)
		for i := range weights {
			weights[i] = rng.Intn(7) - 3
		}
		if got, want := Point(weights), hiddenBrute(weights); got != want {
			t.Fatalf("round %d: Point(%v) = %d, want %d", round, weights, got, want)
		}
	}
}

func TestHiddenLongRow(t *testing.T) {
	const n = 999_999
	ones := make([]int, n)
	for i := range ones {
		ones[i] = 1
	}
	// n-1 alternating weights sum to zero, so only the last index balances.
	tail := make([]int, n)
	for i := range tail[:n-1] {
		if i%2 == 0 {
			tail[i] = 1
		} else {
			tail[i] = -1
		}
	}
	tail[n-1] = 5
	run := func(name string, weights []int, want int) {
		done := make(chan int, 1)
		go func() { done <- Point(weights) }()
		select {
		case got := <-done:
			if got != want {
				t.Fatalf("%s: Point on %d weights = %d, want %d", name, len(weights), got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: Point took longer than 10s on %d weights", name, len(weights))
		}
	}
	run("odd count of ones", ones, n/2)
	run("even count of ones", ones[:n-1], -1)
	run("balances at the end", tail, n-1)
}
