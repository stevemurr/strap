package receipts

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name string
		ids  []int
		want int
	}{
		{"readme scattered", []int{4, 1, 2, 1, 2}, 4},
		{"readme last", []int{2, 2, 1}, 1},
		{"readme single", []int{7}, 7},
		{"readme negative", []int{-3, 5, 5}, -3},
		{"readme zero pair", []int{0, 9, 0}, 9},
		{"readme middle", []int{1, 2, 3, 2, 1}, 3},
		{"single zero", []int{0}, 0},
		{"single negative", []int{-8}, -8},
		{"unmatched zero", []int{5, 0, 5}, 0},
		{"negative pairs", []int{-1, -2, -1, -2, -7}, -7},
		{"large values", []int{1 << 30, 3, 1 << 30}, 3},
		{"adjacent pairs", []int{9, 9, 8, 8, 6, 7, 7}, 6},
		{"mixed signs", []int{-4, 4, -4, 4, -9, 12, 12}, -9},
		{"first element", []int{11, 3, 3, 5, 5}, 11},
	}
	for _, c := range cases {
		if got := Unmatched(c.ids); got != c.want {
			t.Errorf("%s: Unmatched(%v) = %d, want %d", c.name, c.ids, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	ids := []int{3, 1, 3, 2, 1}
	before := append([]int(nil), ids...)
	Unmatched(ids)
	if !reflect.DeepEqual(ids, before) {
		t.Fatalf("input was modified: %v", ids)
	}
}

func hiddenBrute(ids []int) int {
	for _, id := range ids {
		n := 0
		for _, other := range ids {
			if other == id {
				n++
			}
		}
		if n == 1 {
			return id
		}
	}
	return 0
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(136))
	for round := 0; round < 500; round++ {
		pairs := rng.Intn(13)
		values := rng.Perm(60)
		ids := make([]int, 0, 2*pairs+1)
		for _, v := range values[:pairs] {
			ids = append(ids, v-30, v-30)
		}
		ids = append(ids, values[pairs]-30)
		rng.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
		if got, want := Unmatched(ids), hiddenBrute(ids); got != want {
			t.Fatalf("round %d: Unmatched(%v) = %d, want %d", round, ids, got, want)
		}
	}
}

func TestHiddenLargeFeed(t *testing.T) {
	const n = 1_000_001
	const want = 999_999
	ids := make([]int, n)
	for i := 0; i < n-1; i++ {
		ids[i] = i/2 - 250_000
	}
	ids[n-1] = want
	rand.New(rand.NewSource(1)).Shuffle(n, func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	done := make(chan int, 1)
	go func() { done <- Unmatched(ids) }()
	select {
	case got := <-done:
		if got != want {
			t.Fatalf("Unmatched = %d, want %d", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Unmatched took longer than 10s on %d ids", n)
	}
}
