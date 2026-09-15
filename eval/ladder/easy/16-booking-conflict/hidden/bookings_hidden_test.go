package bookings

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name     string
		bookings [][2]int
		want     bool
	}{
		{"readme", [][2]int{{0, 30}, {5, 10}, {15, 20}}, true},
		{"disjoint unsorted", [][2]int{{7, 10}, {2, 4}}, false},
		{"touching", [][2]int{{1, 5}, {5, 9}}, false},
		{"identical", [][2]int{{2, 6}, {2, 6}}, true},
		{"single", [][2]int{{3, 8}}, false},
		{"empty", nil, false},
		{"touching chain unsorted", [][2]int{{4, 6}, {0, 2}, {6, 8}, {2, 4}}, false},
		{"nested", [][2]int{{0, 10}, {3, 4}}, true},
		{"nested reversed", [][2]int{{3, 4}, {0, 10}}, true},
		{"same start", [][2]int{{5, 6}, {5, 9}}, true},
		{"same end", [][2]int{{1, 9}, {5, 9}}, true},
		{"one minute overlap", [][2]int{{0, 5}, {4, 9}}, true},
		{"negative touching", [][2]int{{-5, 0}, {0, 5}}, false},
		{"negative overlap", [][2]int{{-5, 1}, {0, 5}}, true},
		{"late conflict", [][2]int{{0, 1}, {1, 2}, {2, 3}, {3, 4}, {4, 6}, {5, 7}}, true},
		{"gaps", [][2]int{{10, 20}, {30, 40}, {0, 5}}, false},
		{"long one covers all", [][2]int{{10, 12}, {20, 22}, {0, 100}}, true},
	}
	for _, c := range cases {
		if got := HasConflict(c.bookings); got != c.want {
			t.Errorf("%s: HasConflict(%v) = %v, want %v", c.name, c.bookings, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	bookings := [][2]int{{9, 12}, {1, 3}, {4, 8}, {2, 5}}
	before := append([][2]int(nil), bookings...)
	HasConflict(bookings)
	if !reflect.DeepEqual(bookings, before) {
		t.Fatalf("input modified: %v", bookings)
	}
}

func hiddenBrute(bookings [][2]int) bool {
	for i := range bookings {
		for j := i + 1; j < len(bookings); j++ {
			if bookings[i][0] < bookings[j][1] && bookings[j][0] < bookings[i][1] {
				return true
			}
		}
	}
	return false
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(252))
	for round := 0; round < 2000; round++ {
		n := rng.Intn(7)
		bookings := make([][2]int, n)
		for i := range bookings {
			start := rng.Intn(16) - 4
			bookings[i] = [2]int{start, start + 1 + rng.Intn(4)}
		}
		if got, want := HasConflict(bookings), hiddenBrute(bookings); got != want {
			t.Fatalf("round %d: HasConflict(%v) = %v, want %v", round, bookings, got, want)
		}
	}
}

func TestHiddenLargeSchedule(t *testing.T) {
	const n = 1_000_000
	rng := rand.New(rand.NewSource(7))
	free := make([][2]int, n)
	for i := range free {
		free[i] = [2]int{2 * i, 2*i + 2}
	}
	rng.Shuffle(n, func(i, j int) { free[i], free[j] = free[j], free[i] })
	// Stretch one booking (not the last of the day) by a minute so it runs
	// into the next one.
	clash := append([][2]int(nil), free...)
	idx := n / 2
	if clash[idx][0] == 2*(n-1) {
		idx++
	}
	clash[idx][1]++
	run := func(name string, bookings [][2]int, want bool) {
		done := make(chan bool, 1)
		go func() { done <- HasConflict(bookings) }()
		select {
		case got := <-done:
			if got != want {
				t.Fatalf("%s: HasConflict on %d bookings = %v, want %v", name, n, got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: HasConflict took longer than 10s on %d bookings", name, n)
		}
	}
	run("back to back", free, false)
	run("one overrun", clash, true)
}
