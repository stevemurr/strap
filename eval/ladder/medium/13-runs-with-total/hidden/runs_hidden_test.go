package ledger

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name    string
		amounts []int
		target  int
		want    int
	}{
		{"readme ones", []int{1, 1, 1}, 2, 2},
		{"readme one two three", []int{1, 2, 3}, 3, 2},
		{"readme mixed", []int{3, -1, 4, -1, 5}, 3, 3},
		{"readme zeros", []int{0, 0, 0}, 0, 6},
		{"readme alternating", []int{-2, 2, -2, 2}, 0, 4},
		{"readme single hit", []int{5}, 5, 1},
		{"readme empty", nil, 0, 0},
		{"single miss", []int{5}, 4, 0},
		{"negative target", []int{-1, -1, 2, -1}, -2, 1},
		{"whole ledger", []int{4, -2, 7}, 9, 1},
		{"no runs", []int{1, 2, 3}, 100, 0},
		{"duplicates", []int{2, 2, 2, 2}, 4, 3},
		{"zero target with negatives", []int{1, -1, 1, -1}, 0, 4},
		{"target zero no zero run", []int{1, 2, 3}, 0, 0},
	}
	for _, c := range cases {
		if got := RunsWithTotal(c.amounts, c.target); got != c.want {
			t.Errorf("%s: RunsWithTotal(%v, %d) = %d, want %d", c.name, c.amounts, c.target, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	amounts := []int{3, -1, 4, -1, 5}
	before := append([]int(nil), amounts...)
	RunsWithTotal(amounts, 3)
	if !reflect.DeepEqual(amounts, before) {
		t.Fatalf("input modified: %v", amounts)
	}
}

func brute(amounts []int, target int) int {
	runs := 0
	for i := range amounts {
		sum := 0
		for j := i; j < len(amounts); j++ {
			sum += amounts[j]
			if sum == target {
				runs++
			}
		}
	}
	return runs
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(560))
	for round := 0; round < 500; round++ {
		n := rng.Intn(40)
		spread := []int{3, 5, 11, 100}[round%4]
		amounts := make([]int, n)
		for i := range amounts {
			amounts[i] = rng.Intn(spread) - spread/2
		}
		target := rng.Intn(2*spread+1) - spread
		got, want := RunsWithTotal(amounts, target), brute(amounts, target)
		if got != want {
			t.Fatalf("round %d: RunsWithTotal(%v, %d) = %d, want %d", round, amounts, target, got, want)
		}
	}
}

func TestHiddenLargeLedger(t *testing.T) {
	const n = 300_000
	run := func(name string, amounts []int, target int) int {
		done := make(chan int, 1)
		go func() { done <- RunsWithTotal(amounts, target) }()
		select {
		case got := <-done:
			return got
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: RunsWithTotal took longer than 10s on %d entries", name, n)
			return 0
		}
	}

	zeros := make([]int, n)
	if got, want := run("zeros", zeros, 0), n*(n+1)/2; got != want {
		t.Fatalf("zeros: got %d, want %d", got, want)
	}

	// Prefix sums alternate 0,1,0,1,...: 150001 zeros and 150000 ones.
	alternating := make([]int, n)
	for i := range alternating {
		if i%2 == 0 {
			alternating[i] = 1
		} else {
			alternating[i] = -1
		}
	}
	const evens, odds = n/2 + 1, n / 2
	if got, want := run("alternating", alternating, 0), evens*(evens-1)/2+odds*(odds-1)/2; got != want {
		t.Fatalf("alternating: got %d, want %d", got, want)
	}

	ones := make([]int, n)
	for i := range ones {
		ones[i] = 1
	}
	if got := run("ones", ones, 1000); got != n-1000+1 {
		t.Fatalf("ones: got %d, want %d", got, n-1000+1)
	}
}
