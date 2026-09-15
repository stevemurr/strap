package sharding

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name   string
		sizes  []int
		shards int
		want   int
	}{
		{"readme", []int{7, 2, 5, 10, 8}, 2, 18},
		{"rising", []int{1, 2, 3, 4, 5}, 2, 9},
		{"one each", []int{1, 4, 4}, 3, 4},
		{"merge one pair", []int{2, 3, 1, 2, 4, 3}, 5, 4},
		{"single shard", []int{5, 5}, 1, 10},
		{"zeros", []int{0, 0, 0}, 2, 0},
		{"more shards than batches", []int{3}, 7, 3},
		{"empty", nil, 3, 0},
		{"empty slice", []int{}, 1, 0},
		{"no shards", []int{1, 2}, 0, 0},
		{"negative shards", []int{1, 2}, -2, 0},
		{"big batch dominates", []int{1, 1, 100, 1, 1}, 3, 100},
		{"zeros between", []int{4, 0, 0, 4, 0, 4}, 3, 4},
		{"three shards", []int{10, 5, 13, 4, 8, 4, 5, 11, 14, 9, 16, 10, 11, 8, 9}, 3, 50},
	}
	for _, c := range cases {
		if got := MinLargestShard(c.sizes, c.shards); got != c.want {
			t.Errorf("%s: MinLargestShard(%v, %d) = %d, want %d", c.name, c.sizes, c.shards, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	sizes := []int{9, 4, 7, 1, 4, 3}
	before := append([]int(nil), sizes...)
	MinLargestShard(sizes, 3)
	if !reflect.DeepEqual(sizes, before) {
		t.Fatalf("input modified: %v", sizes)
	}
}

// brute is the O(n^2 * shards) dynamic programme over prefixes.
func brute(sizes []int, shards int) int {
	n := len(sizes)
	if shards < 1 || n == 0 {
		return 0
	}
	if shards > n {
		shards = n
	}
	const inf = 1 << 60
	prev := make([]int, n+1) // prev[i]: best largest total splitting sizes[:i] into g-1 groups
	for i := range prev {
		prev[i] = inf
	}
	prev[0] = 0
	for g := 1; g <= shards; g++ {
		cur := make([]int, n+1)
		for i := range cur {
			cur[i] = inf
		}
		for i := 1; i <= n; i++ {
			total := 0
			for j := i; j >= 1; j-- {
				total += sizes[j-1]
				if prev[j-1] < inf {
					if v := max(prev[j-1], total); v < cur[i] {
						cur[i] = v
					}
				}
			}
		}
		prev = cur
	}
	return prev[n]
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(410))
	for round := 0; round < 400; round++ {
		n := rng.Intn(12) + 1
		sizes := make([]int, n)
		spread := []int{2, 5, 30, 1000}[round%4]
		for i := range sizes {
			sizes[i] = rng.Intn(spread)
		}
		shards := rng.Intn(n+2) - 1
		got, want := MinLargestShard(sizes, shards), brute(sizes, shards)
		if got != want {
			t.Fatalf("round %d: MinLargestShard(%v, %d) = %d, want %d", round, sizes, shards, got, want)
		}
	}
}

func TestHiddenLargeIndex(t *testing.T) {
	const n = 1_000_000
	ones := make([]int, n)
	alternating := make([]int, n)
	oneHeavy := make([]int, n)
	for i := range ones {
		ones[i] = 1
		alternating[i] = i % 2
		oneHeavy[i] = 1
	}
	oneHeavy[n/2] = 5000
	for _, shape := range []struct {
		name   string
		sizes  []int
		shards int
		want   int
	}{
		{"ones into 1000", ones, 1000, 1000},
		{"ones into 10", ones, 10, 100_000},
		{"alternating into 1000", alternating, 1000, 500},
		{"one heavy batch into 1000", oneHeavy, 1000, 5000},
	} {
		done := make(chan int, 1)
		go func() { done <- MinLargestShard(shape.sizes, shape.shards) }()
		select {
		case got := <-done:
			if got != shape.want {
				t.Fatalf("%s: got %d, want %d", shape.name, got, shape.want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: MinLargestShard took longer than 10s on %d batches", shape.name, n)
		}
	}
}
