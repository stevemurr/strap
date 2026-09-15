package prices

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name   string
		prices []int
		want   []int
	}{
		{"readme", []int{73, 74, 75, 71, 69, 72, 76, 73}, []int{1, 1, 4, 2, 1, 1, 0, 0}},
		{"increasing", []int{30, 40, 50, 60}, []int{1, 1, 1, 0}},
		{"three", []int{30, 60, 90}, []int{1, 1, 0}},
		{"flat", []int{5, 5, 5}, []int{0, 0, 0}},
		{"decreasing", []int{9, 8, 7, 6}, []int{0, 0, 0, 0}},
		{"single", []int{42}, []int{0}},
		{"negatives", []int{-3, -5, -4, -1}, []int{3, 1, 1, 0}},
		{"equal then higher", []int{2, 2, 3}, []int{2, 1, 0}},
		{"valley", []int{5, 1, 2, 3, 4, 6}, []int{5, 1, 1, 1, 1, 0}},
		{"zigzag", []int{1, 3, 2, 4, 3, 5}, []int{1, 2, 1, 2, 1, 0}},
	}
	for _, c := range cases {
		got := DaysUntilHigher(c.prices)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: DaysUntilHigher(%v) = %v, want %v", c.name, c.prices, got, c.want)
		}
	}
}

func TestHiddenEmpty(t *testing.T) {
	if got := DaysUntilHigher(nil); len(got) != 0 {
		t.Fatalf("DaysUntilHigher(nil) = %v, want empty", got)
	}
	if got := DaysUntilHigher([]int{}); len(got) != 0 {
		t.Fatalf("DaysUntilHigher([]) = %v, want empty", got)
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	prices := []int{7, 3, 9, 3, 8, 1}
	before := append([]int(nil), prices...)
	DaysUntilHigher(prices)
	if !reflect.DeepEqual(prices, before) {
		t.Fatalf("input modified: %v", prices)
	}
}

func brute(prices []int) []int {
	out := make([]int, len(prices))
	for i, p := range prices {
		for j := i + 1; j < len(prices); j++ {
			if prices[j] > p {
				out[i] = j - i
				break
			}
		}
	}
	return out
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(739))
	for round := 0; round < 400; round++ {
		n := rng.Intn(50) + 1
		spread := []int{2, 5, 40, 1000}[round%4]
		prices := make([]int, n)
		for i := range prices {
			prices[i] = rng.Intn(spread) - spread/2
		}
		got, want := DaysUntilHigher(prices), brute(prices)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round %d: DaysUntilHigher(%v) = %v, want %v", round, prices, got, want)
		}
	}
}

func TestHiddenLargeInput(t *testing.T) {
	const n = 1_000_000
	decreasing := make([]int, n)
	increasing := make([]int, n)
	sawtooth := make([]int, n)
	for i := range decreasing {
		decreasing[i] = n - i
		increasing[i] = i - n/2
		sawtooth[i] = i % 1000
	}
	run := func(name string, prices []int) []int {
		done := make(chan []int, 1)
		go func() { done <- DaysUntilHigher(prices) }()
		select {
		case got := <-done:
			if len(got) != n {
				t.Fatalf("%s: got %d entries, want %d", name, len(got), n)
			}
			return got
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: DaysUntilHigher took longer than 10s on %d prices", name, n)
			return nil
		}
	}
	got := run("decreasing", decreasing)
	for i, v := range got {
		if v != 0 {
			t.Fatalf("decreasing: out[%d] = %d, want 0", i, v)
		}
	}
	got = run("increasing", increasing)
	for i, v := range got {
		want := 1
		if i == n-1 {
			want = 0
		}
		if v != want {
			t.Fatalf("increasing: out[%d] = %d, want %d", i, v, want)
		}
	}
	got = run("sawtooth", sawtooth)
	for i, v := range got {
		want := 1
		if i%1000 == 999 {
			want = 0
		}
		if v != want {
			t.Fatalf("sawtooth: out[%d] = %d, want %d", i, v, want)
		}
	}
}
