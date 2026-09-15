package uptime

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		up   []bool
		want int
	}{
		{[]bool{true, true, false, true, true, true}, 3},
		{[]bool{true, false, true}, 1},
		{[]bool{true, true, true, true}, 4},
		{[]bool{false, false}, 0},
		{[]bool{true}, 1},
		{nil, 0},
		{[]bool{}, 0},
		{[]bool{false}, 0},
		{[]bool{true, true, false, true, true}, 2},
		{[]bool{false, true, true, true, false}, 3},
		{[]bool{true, true, true, false, true}, 3},
		{[]bool{true, false, true, true, true}, 3},
		{[]bool{false, false, true}, 1},
		{[]bool{true, false, false, true, true}, 2},
		{[]bool{true, false, true, true, false, true, true, true, false}, 3},
		{[]bool{false, true, false, true, false}, 1},
	}
	for _, c := range cases {
		if got := Longest(c.up); got != c.want {
			t.Errorf("Longest(%v) = %d, want %d", c.up, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	up := []bool{true, false, true, true, false}
	before := append([]bool(nil), up...)
	Longest(up)
	if !reflect.DeepEqual(up, before) {
		t.Fatalf("input modified: %v", up)
	}
}

func hiddenBrute(up []bool) int {
	best := 0
	for start := range up {
		n := 0
		for _, ok := range up[start:] {
			if !ok {
				break
			}
			n++
		}
		if n > best {
			best = n
		}
	}
	return best
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(485))
	for round := 0; round < 3000; round++ {
		n := rng.Intn(30)
		up := make([]bool, n)
		bias := []int{25, 50, 75, 95}[round%4]
		for i := range up {
			up[i] = rng.Intn(100) < bias
		}
		if got, want := Longest(up), hiddenBrute(up); got != want {
			t.Fatalf("round %d: Longest(%v) = %d, want %d", round, up, got, want)
		}
	}
}

func TestHiddenLongHistory(t *testing.T) {
	const n = 10_000_000
	periodic := make([]bool, n)
	for i := range periodic {
		periodic[i] = i%1000 != 0
	}
	allUp := make([]bool, n)
	for i := range allUp {
		allUp[i] = true
	}
	allDown := make([]bool, n)
	run := func(name string, up []bool, want int) {
		done := make(chan int, 1)
		go func() { done <- Longest(up) }()
		select {
		case got := <-done:
			if got != want {
				t.Fatalf("%s: Longest on %d samples = %d, want %d", name, n, got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: Longest took longer than 10s on %d samples", name, n)
		}
	}
	run("one failure every 1000", periodic, 999)
	run("all up", allUp, n)
	run("all down", allDown, 0)
}
