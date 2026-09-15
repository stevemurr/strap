package ringlog

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name   string
		ids    []int
		target int
		want   int
	}{
		{"readme hit", []int{4, 5, 6, 7, 0, 1, 2}, 0, 4},
		{"readme miss", []int{4, 5, 6, 7, 0, 1, 2}, 3, -1},
		{"readme sorted", []int{1, 2, 3, 4, 5}, 5, 4},
		{"readme pair", []int{3, 1}, 1, 1},
		{"readme single", []int{7}, 7, 0},
		{"readme empty", nil, 9, -1},
		{"single miss", []int{7}, 8, -1},
		{"first slot", []int{4, 5, 6, 7, 0, 1, 2}, 4, 0},
		{"last slot", []int{4, 5, 6, 7, 0, 1, 2}, 2, 6},
		{"pivot", []int{4, 5, 6, 7, 0, 1, 2}, 7, 3},
		{"after pivot", []int{4, 5, 6, 7, 0, 1, 2}, 0, 4},
		{"below everything", []int{4, 5, 6, 7, 0, 1, 2}, -1, -1},
		{"above everything", []int{4, 5, 6, 7, 0, 1, 2}, 8, -1},
		{"in the gap", []int{10, 20, 30, 1, 2}, 25, -1},
		{"gap hit", []int{10, 20, 30, 1, 2}, 30, 2},
		{"gap miss", []int{10, 20, 30, 1, 2}, 5, -1},
		{"negatives", []int{2, 5, -9, -4, 0}, -4, 3},
		{"rotated by one", []int{5, 1, 2, 3, 4}, 5, 0},
		{"rotated by n-1", []int{2, 3, 4, 5, 1}, 1, 4},
		{"pair sorted", []int{1, 3}, 3, 1},
		{"pair miss", []int{3, 1}, 2, -1},
	}
	for _, c := range cases {
		if got := Find(c.ids, c.target); got != c.want {
			t.Errorf("%s: Find(%v, %d) = %d, want %d", c.name, c.ids, c.target, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	ids := []int{4, 5, 6, 7, 0, 1, 2}
	before := append([]int(nil), ids...)
	Find(ids, 6)
	Find(ids, 9)
	if !reflect.DeepEqual(ids, before) {
		t.Fatalf("input modified: %v", ids)
	}
}

func scan(ids []int, target int) int {
	for i, v := range ids {
		if v == target {
			return i
		}
	}
	return -1
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(33))
	for round := 0; round < 1000; round++ {
		n := rng.Intn(30) + 1
		// Distinct ascending values with random gaps, then rotate.
		sorted := make([]int, n)
		v := rng.Intn(20) - 10
		for i := range sorted {
			sorted[i] = v
			v += rng.Intn(3) + 1
		}
		rot := rng.Intn(n)
		ids := append(append([]int(nil), sorted[rot:]...), sorted[:rot]...)
		for q := 0; q < 8; q++ {
			target := rng.Intn(v+12) - 11
			if q%2 == 0 {
				target = ids[rng.Intn(n)]
			}
			got, want := Find(ids, target), scan(ids, target)
			if got != want {
				t.Fatalf("round %d: Find(%v, %d) = %d, want %d", round, ids, target, got, want)
			}
		}
	}
}

func TestHiddenManyLookups(t *testing.T) {
	const n = 1_000_000
	const rot = 400_000
	const lookups = 200_000
	ids := make([]int, n)
	for i := range ids {
		ids[i] = 2 * ((i + rot) % n)
	}
	rng := rand.New(rand.NewSource(330))
	targets := make([]int, lookups)
	for i := range targets {
		targets[i] = rng.Intn(2*n+2) - 1
	}
	done := make(chan error, 1)
	go func() {
		for _, target := range targets {
			want := -1
			if target >= 0 && target < 2*n && target%2 == 0 {
				want = ((target/2-rot)%n + n) % n
			}
			if got := Find(ids, target); got != want {
				done <- fmt.Errorf("Find(ids, %d) = %d, want %d", target, got, want)
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("%d lookups over %d ids took longer than 10s", lookups, n)
	}
}
