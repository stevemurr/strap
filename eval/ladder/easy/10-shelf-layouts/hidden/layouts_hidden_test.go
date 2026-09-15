package shelving

import (
	"math/rand"
	"testing"
	"time"
)

// call runs Layouts with a deadline so an exponential solution fails instead
// of hanging the test binary.
func call(t *testing.T, width int) int64 {
	t.Helper()
	done := make(chan int64, 1)
	go func() { done <- Layouts(width) }()
	select {
	case v := <-done:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("Layouts(%d) took longer than 10s", width)
		return 0
	}
}

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		width int
		want  int64
	}{
		{-1, 0}, {0, 1}, {1, 1}, {2, 2}, {3, 3}, {4, 5}, {5, 8}, {6, 13}, {7, 21}, {8, 34}, {9, 55}, {10, 89},
		{11, 144}, {12, 233}, {13, 377}, {14, 610}, {15, 987},
		{30, 1346269}, {45, 1836311903}, {60, 2504730781961}, {75, 3416454622906707},
		{89, 2880067194370816120}, {90, 4660046610375530309},
	}
	for _, c := range cases {
		if got := call(t, c.width); got != c.want {
			t.Errorf("Layouts(%d) = %d, want %d", c.width, got, c.want)
		}
	}
}

func TestHiddenNegativeWidths(t *testing.T) {
	for _, width := range []int{-1, -2, -3, -50, -1000} {
		if got := call(t, width); got != 0 {
			t.Errorf("Layouts(%d) = %d, want 0", width, got)
		}
	}
}

func TestHiddenRecurrence(t *testing.T) {
	prev, cur := call(t, 0), call(t, 1)
	for width := 2; width <= 90; width++ {
		next := call(t, width)
		if next != prev+cur {
			t.Fatalf("Layouts(%d) = %d, want %d + %d", width, next, prev, cur)
		}
		if next <= cur {
			t.Fatalf("Layouts(%d) = %d is not larger than Layouts(%d) = %d", width, next, width-1, cur)
		}
		prev, cur = cur, next
	}
}

// brute enumerates every choice of next box directly from the definition.
func brute(width int) int64 {
	if width < 0 {
		return 0
	}
	if width == 0 {
		return 1
	}
	var n int64
	for _, box := range []int{1, 2} {
		if box <= width {
			n += brute(width - box)
		}
	}
	return n
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(70))
	for round := 0; round < 200; round++ {
		width := rng.Intn(30) - 4
		if got, want := call(t, width), brute(width); got != want {
			t.Fatalf("round %d: Layouts(%d) = %d, want %d", round, width, got, want)
		}
	}
}

func TestHiddenWholeRange(t *testing.T) {
	done := make(chan int64, 1)
	go func() {
		var last int64
		for width := -5; width <= 90; width++ {
			last = Layouts(width)
		}
		done <- last
	}()
	select {
	case got := <-done:
		if got != 4660046610375530309 {
			t.Fatalf("Layouts(90) = %d, want 4660046610375530309", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("computing Layouts for every width from -5 to 90 took longer than 10s")
	}
}
