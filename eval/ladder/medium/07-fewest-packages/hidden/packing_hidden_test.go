package packing

import (
	"math/rand"
	"slices"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name     string
		sizes    []int
		quantity int
		want     int
	}{
		{"readme", []int{1, 2, 5}, 11, 3},
		{"greedy is wrong", []int{1, 3, 4}, 6, 2},
		{"impossible", []int{2}, 3, -1},
		{"impossible pair", []int{3, 7}, 5, -1},
		{"zero quantity", []int{5, 10}, 0, 0},
		{"negative quantity", []int{4, 6}, -2, -1},
		{"no sizes", nil, 5, -1},
		{"no sizes zero quantity", nil, 0, 0},
		{"exact single package", []int{7}, 7, 1},
		{"size larger than quantity", []int{9}, 4, -1},
		{"duplicate sizes", []int{2, 2, 2}, 8, 4},
		{"unsorted sizes", []int{25, 1, 10, 5}, 30, 2},
		{"greedy is wrong again", []int{1, 5, 6, 9}, 11, 2},
		{"only ones", []int{1}, 13, 13},
		{"parity blocks", []int{4, 8, 12}, 26, -1},
	}
	for _, c := range cases {
		if got := FewestPackages(c.sizes, c.quantity); got != c.want {
			t.Errorf("%s: FewestPackages(%v, %d) = %d, want %d", c.name, c.sizes, c.quantity, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	sizes := []int{9, 4, 7, 1}
	before := slices.Clone(sizes)
	FewestPackages(sizes, 20)
	if !slices.Equal(sizes, before) {
		t.Fatalf("input modified: %v", sizes)
	}
}

// bruteFewest tries every combination recursively.
func bruteFewest(sizes []int, quantity int) int {
	if quantity < 0 {
		return -1
	}
	if quantity == 0 {
		return 0
	}
	best := -1
	for _, s := range sizes {
		if s > quantity {
			continue
		}
		if sub := bruteFewest(sizes, quantity-s); sub >= 0 && (best < 0 || sub+1 < best) {
			best = sub + 1
		}
	}
	return best
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(322))
	for round := 0; round < 300; round++ {
		sizes := make([]int, rng.Intn(5))
		for i := range sizes {
			sizes[i] = 1 + rng.Intn(9)
		}
		quantity := rng.Intn(21) - 2
		if got, want := FewestPackages(sizes, quantity), bruteFewest(sizes, quantity); got != want {
			t.Fatalf("round %d: FewestPackages(%v, %d) = %d, want %d", round, sizes, quantity, got, want)
		}
	}
}

func TestHiddenLargeQuantity(t *testing.T) {
	// Fifty consecutive sizes 1000..1049: with m packages every total from
	// 1000m to 1049m is reachable, so the answer is the smallest m whose range
	// contains the quantity. Taking the largest size first would fail.
	consecutive := make([]int, 50)
	for i := range consecutive {
		consecutive[i] = 1000 + i
	}
	// Fifty even sizes can never sum to an odd quantity.
	even := make([]int, 50)
	for i := range even {
		even[i] = 1000 + 2*i
	}
	// A size of 1 makes everything reachable and forces the deepest chain.
	withOne := append([]int{1}, consecutive[:49]...)

	for _, shape := range []struct {
		name     string
		sizes    []int
		quantity int
		want     int
	}{
		{"consecutive sizes", consecutive, 100_000, 96},
		{"consecutive sizes shuffled", shuffled(consecutive, 7), 100_000, 96},
		{"even sizes odd quantity", even, 99_999, -1},
		{"with size one", withOne, 100_000, 96},
		{"just below reach", consecutive, 99_999, 96},
		{"between ranges", consecutive, 20_999, -1},
	} {
		done := make(chan int, 1)
		go func() { done <- FewestPackages(shape.sizes, shape.quantity) }()
		select {
		case got := <-done:
			if got != shape.want {
				t.Fatalf("%s: got %d, want %d", shape.name, got, shape.want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: FewestPackages took longer than 10s for quantity %d", shape.name, shape.quantity)
		}
	}
}

func shuffled(sizes []int, seed int64) []int {
	out := slices.Clone(sizes)
	rand.New(rand.NewSource(seed)).Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}
