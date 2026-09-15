package pricehistory

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
		{"readme", []int{5, 2, 6, 1}, []int{2, 1, 1, 0}},
		{"decreasing", []int{3, 2, 1}, []int{2, 1, 0}},
		{"increasing", []int{1, 2, 3}, []int{0, 0, 0}},
		{"equal", []int{-1, -1}, []int{0, 0}},
		{"single", []int{4}, []int{0}},
		{"negatives", []int{-1, -5, 3, -5, 0}, []int{2, 0, 2, 0, 0}},
		{"duplicates", []int{2, 2, 1, 1, 2}, []int{2, 2, 0, 0, 0}},
		{"zeros", []int{0, 0, 0}, []int{0, 0, 0}},
		{"extremes", []int{1_000_000_000, -1_000_000_000, 0}, []int{2, 0, 0}},
	}
	for _, c := range cases {
		got := LaterLowerCounts(c.prices)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: LaterLowerCounts(%v) = %v, want %v", c.name, c.prices, got, c.want)
		}
	}
}

func TestHiddenEmpty(t *testing.T) {
	if got := LaterLowerCounts(nil); len(got) != 0 {
		t.Fatalf("LaterLowerCounts(nil) = %v, want empty", got)
	}
	if got := LaterLowerCounts([]int{}); len(got) != 0 {
		t.Fatalf("LaterLowerCounts([]) = %v, want empty", got)
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	prices := []int{9, 4, 7, 1, 4, -3}
	before := append([]int(nil), prices...)
	LaterLowerCounts(prices)
	if !reflect.DeepEqual(prices, before) {
		t.Fatalf("input modified: %v", prices)
	}
}

func brute(prices []int) []int {
	out := make([]int, len(prices))
	for i, p := range prices {
		for _, q := range prices[i+1:] {
			if q < p {
				out[i]++
			}
		}
	}
	return out
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(315))
	for round := 0; round < 400; round++ {
		n := rng.Intn(60) + 1
		prices := make([]int, n)
		spread := []int{3, 10, 100, 1_000_000_000}[round%4]
		for i := range prices {
			prices[i] = rng.Intn(spread) - spread/2
		}
		got, want := LaterLowerCounts(prices), brute(prices)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round %d: LaterLowerCounts(%v) = %v, want %v", round, prices, got, want)
		}
	}
}

func TestHiddenLargeSeries(t *testing.T) {
	const n = 500_000
	decreasing := make([]int, n)
	random := make([]int, n)
	rng := rand.New(rand.NewSource(7))
	for i := range decreasing {
		decreasing[i] = n - i
		random[i] = rng.Intn(2_000_000_001) - 1_000_000_000
	}
	for _, shape := range []struct {
		name string
		data []int
	}{
		{"decreasing", decreasing},
		{"random", random},
	} {
		done := make(chan []int, 1)
		go func() { done <- LaterLowerCounts(shape.data) }()
		var got []int
		select {
		case got = <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: LaterLowerCounts took longer than 10s on %d prices", shape.name, n)
		}
		if len(got) != n {
			t.Fatalf("%s: got %d counts, want %d", shape.name, len(got), n)
		}
		for _, i := range []int{0, 1, 12345, n / 2, n - 2, n - 1} {
			want := 0
			for _, q := range shape.data[i+1:] {
				if q < shape.data[i] {
					want++
				}
			}
			if got[i] != want {
				t.Fatalf("%s: count[%d] = %d, want %d", shape.name, i, got[i], want)
			}
		}
	}
}
