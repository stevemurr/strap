package tickets

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		issued []int
		want   int
	}{
		{[]int{3, 0, 1}, 2},
		{[]int{0, 1}, 2},
		{[]int{1}, 0},
		{[]int{9, 6, 4, 2, 3, 5, 7, 0, 1}, 8},
		{nil, 0},
		{[]int{}, 0},
		{[]int{0}, 1},
		{[]int{1, 0}, 2},
		{[]int{2, 0}, 1},
		{[]int{1, 2}, 0},
		{[]int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, 10},
		{[]int{10, 9, 8, 7, 6, 5, 4, 3, 2, 1}, 0},
		{[]int{5, 3, 0, 1, 4, 2, 7}, 6},
	}
	for _, c := range cases {
		if got := Missing(c.issued); got != c.want {
			t.Errorf("Missing(%v) = %d, want %d", c.issued, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	issued := []int{4, 0, 3, 1}
	before := append([]int(nil), issued...)
	Missing(issued)
	if !reflect.DeepEqual(issued, before) {
		t.Fatalf("input modified: %v", issued)
	}
}

func hiddenBrute(issued []int) int {
	for candidate := 0; candidate <= len(issued); candidate++ {
		found := false
		for _, v := range issued {
			if v == candidate {
				found = true
				break
			}
		}
		if !found {
			return candidate
		}
	}
	return -1
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(268))
	for round := 0; round < 500; round++ {
		n := rng.Intn(40)
		perm := rng.Perm(n + 1)
		issued := perm[:n] // perm[n] is the number left out
		got, want := Missing(issued), hiddenBrute(issued)
		if want != perm[n] {
			t.Fatalf("round %d: oracle disagrees with the construction", round)
		}
		if got != want {
			t.Fatalf("round %d: Missing(%v) = %d, want %d", round, issued, got, want)
		}
	}
}

func TestHiddenLargeLog(t *testing.T) {
	const n = 1_000_000
	rng := rand.New(rand.NewSource(1))
	perm := rng.Perm(n + 1)
	for _, missing := range []int{0, n / 3, n} {
		issued := make([]int, 0, n)
		for _, v := range perm {
			if v != missing {
				issued = append(issued, v)
			}
		}
		done := make(chan int, 1)
		go func() { done <- Missing(issued) }()
		select {
		case got := <-done:
			if got != missing {
				t.Fatalf("Missing on %d entries = %d, want %d", n, got, missing)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("Missing took longer than 10s on %d entries", n)
		}
	}
}
