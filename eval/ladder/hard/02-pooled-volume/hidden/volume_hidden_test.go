package terrain

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
		{"readme classic", []int{0, 1, 0, 2, 1, 0, 1, 3, 2, 1, 2, 1}, 6},
		{"readme second", []int{4, 2, 0, 3, 2, 5}, 9},
		{"cup", []int{3, 0, 3}, 3},
		{"wide cup", []int{5, 0, 0, 0, 5}, 15},
		{"rising", []int{1, 2, 3}, 0},
		{"two columns", []int{2, 2}, 0},
		{"empty", nil, 0},
		{"single", []int{7}, 0},
		{"falling", []int{3, 2, 1}, 0},
		{"flat", []int{4, 4, 4, 4}, 0},
		{"zeros", []int{0, 0, 0}, 0},
		{"lower left wall", []int{2, 0, 5}, 2},
		{"lower right wall", []int{5, 0, 2}, 2},
		{"two basins", []int{3, 0, 2, 0, 4}, 7},
		{"wide floor", []int{4, 1, 1, 4}, 6},
		{"peak in middle", []int{1, 0, 9, 0, 1}, 2},
		{"tallest at ends", []int{9, 8, 7, 8, 9}, 4},
		{"nothing to hold", []int{0, 9, 0}, 0},
	}
	for _, c := range cases {
		if got := PooledVolume(c.heights); got != c.want {
			t.Errorf("%s: PooledVolume(%v) = %d, want %d", c.name, c.heights, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	heights := []int{4, 2, 0, 3, 2, 5}
	before := append([]int(nil), heights...)
	PooledVolume(heights)
	if !reflect.DeepEqual(heights, before) {
		t.Fatalf("input modified: %v", heights)
	}
}

// brute finds each column's bounding walls by scanning outward from it.
func brute(heights []int) int {
	total := 0
	for i, h := range heights {
		leftMax, rightMax := h, h
		for _, v := range heights[:i] {
			if v > leftMax {
				leftMax = v
			}
		}
		for _, v := range heights[i+1:] {
			if v > rightMax {
				rightMax = v
			}
		}
		if leftMax < rightMax {
			total += leftMax - h
		} else {
			total += rightMax - h
		}
	}
	return total
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for round := 0; round < 400; round++ {
		n := rng.Intn(40)
		spread := []int{2, 5, 100}[round%3]
		heights := make([]int, n)
		for i := range heights {
			heights[i] = rng.Intn(spread)
		}
		if got, want := PooledVolume(heights), brute(heights); got != want {
			t.Fatalf("round %d: PooledVolume(%v) = %d, want %d", round, heights, got, want)
		}
	}
}

// byWalls is a linear check used only at scale: prefix maxima in one pass,
// suffix maxima folded into the second.
func byWalls(heights []int) int {
	if len(heights) == 0 {
		return 0
	}
	prefix := make([]int, len(heights))
	m := 0
	for i, h := range heights {
		if h > m {
			m = h
		}
		prefix[i] = m
	}
	total, m := 0, 0
	for i := len(heights) - 1; i >= 0; i-- {
		if heights[i] > m {
			m = heights[i]
		}
		level := m
		if prefix[i] < level {
			level = prefix[i]
		}
		total += level - heights[i]
	}
	return total
}

func TestHiddenLargeProfile(t *testing.T) {
	const n = 5_000_000
	random := make([]int, n)
	x := uint32(11)
	for i := range random {
		x = x*1664525 + 1013904223
		random[i] = int(x>>12) % 1_000_001
	}
	bowl := make([]int, n)
	for i := range bowl {
		if i < n/2 {
			bowl[i] = n/2 - i
		} else {
			bowl[i] = i - n/2
		}
	}
	for _, shape := range []struct {
		name string
		data []int
	}{
		{"random", random},
		{"bowl", bowl},
	} {
		done := make(chan int, 1)
		go func() { done <- PooledVolume(shape.data) }()
		var got int
		select {
		case got = <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: PooledVolume took longer than 10s on %d columns", shape.name, n)
		}
		if want := byWalls(shape.data); got != want {
			t.Fatalf("%s: PooledVolume = %d, want %d", shape.name, got, want)
		}
	}
}
