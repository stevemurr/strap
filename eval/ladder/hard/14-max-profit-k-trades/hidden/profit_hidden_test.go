package trading

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
		k      int
		want   int
	}{
		{"readme", []int{2, 4, 1}, 2, 2},
		{"two trades", []int{3, 2, 6, 5, 0, 3}, 2, 7},
		{"rising", []int{1, 2, 3, 4, 5}, 2, 4},
		{"falling", []int{7, 6, 4, 3, 1}, 3, 0},
		{"one trade", []int{1, 5, 2, 8}, 1, 7},
		{"unlimited", []int{1, 5, 2, 8}, 100, 10},
		{"single day", []int{5}, 1, 0},
		{"empty", nil, 3, 0},
		{"k zero", []int{1, 9}, 0, 0},
		{"k negative", []int{1, 9}, -1, 0},
		{"flat", []int{4, 4, 4, 4}, 2, 0},
		{"k exceeds useful trades", []int{1, 3, 1, 3, 1, 3}, 2, 4},
		{"k covers all rises", []int{1, 3, 1, 3, 1, 3}, 3, 6},
		{"skip a dip", []int{1, 10, 9, 20}, 1, 19},
		{"take the dip", []int{1, 10, 9, 20}, 2, 20},
		{"zeros", []int{0, 0, 5, 0}, 1, 5},
	}
	for _, c := range cases {
		if got := MaxProfit(c.prices, c.k); got != c.want {
			t.Errorf("%s: MaxProfit(%v, %d) = %d, want %d", c.name, c.prices, c.k, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	prices := []int{3, 1, 4, 1, 5, 9, 2, 6}
	before := append([]int(nil), prices...)
	MaxProfit(prices, 3)
	if !reflect.DeepEqual(prices, before) {
		t.Fatalf("input modified: %v", prices)
	}
}

// brute explores every legal sequence of buys and sells day by day.
func brute(prices []int, k int) int {
	var rec func(day, left int, holding bool, cost int) int
	rec = func(day, left int, holding bool, cost int) int {
		if day == len(prices) {
			return 0
		}
		best := rec(day+1, left, holding, cost)
		if holding {
			if v := prices[day] - cost + rec(day+1, left, false, 0); v > best {
				best = v
			}
		} else if left > 0 {
			if v := rec(day+1, left-1, true, prices[day]); v > best {
				best = v
			}
		}
		return best
	}
	if k < 1 {
		return 0
	}
	return rec(0, k, false, 0)
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(188))
	for round := 0; round < 300; round++ {
		n := rng.Intn(11) + 1
		prices := make([]int, n)
		spread := []int{3, 10, 100}[round%3]
		for i := range prices {
			prices[i] = rng.Intn(spread)
		}
		k := rng.Intn(n + 2)
		got, want := MaxProfit(prices, k), brute(prices, k)
		if got != want {
			t.Fatalf("round %d: MaxProfit(%v, %d) = %d, want %d", round, prices, k, got, want)
		}
	}
}

func TestHiddenLargeSeries(t *testing.T) {
	const n = 100_000
	// hills: 100 rises of 1,000 days each, hill h climbing from 0 to a gain
	// that is a permutation of 1..100. With k trades the best plan takes the
	// k largest hills, so k = 50 earns 51+52+...+100 = 3775.
	hills := make([]int, n)
	for h := 0; h < 100; h++ {
		gain := (h*7919)%100 + 1
		for d := 0; d < 1000; d++ {
			hills[h*1000+d] = gain * d / 999
		}
	}
	rng := rand.New(rand.NewSource(4))
	random := make([]int, n)
	for i := range random {
		random[i] = rng.Intn(1000)
	}
	unlimited := 0
	for i := 1; i < n; i++ {
		if d := random[i] - random[i-1]; d > 0 {
			unlimited += d
		}
	}
	for _, shape := range []struct {
		name   string
		prices []int
		k      int
		want   int
	}{
		{"hills k=50", hills, 50, 3775},
		{"hills k=1000000", hills, 1_000_000, 5050},
		{"random k=1000000", random, 1_000_000, unlimited},
	} {
		done := make(chan int, 1)
		go func() { done <- MaxProfit(shape.prices, shape.k) }()
		select {
		case got := <-done:
			if got != shape.want {
				t.Fatalf("%s: got %d, want %d", shape.name, got, shape.want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: MaxProfit took longer than 10s on %d days", shape.name, n)
		}
	}
}
