package rollingpeak

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		readings []int
		k        int
		want     []int
	}{
		{[]int{1, 3, -1, -3, 5, 3, 6, 7}, 3, []int{3, 3, 5, 5, 6, 7}},
		{[]int{4, 4, 4}, 2, []int{4, 4}},
		{[]int{9, 8, 7}, 1, []int{9, 8, 7}},
		{[]int{9, 8, 7}, 3, []int{9}},
		{[]int{1, 2}, 3, nil},
		{nil, 1, nil},
		{[]int{5}, 0, nil},
		{[]int{5}, -2, nil},
		{[]int{5}, 1, []int{5}},
		{[]int{-7, -3, -9, -1}, 2, []int{-3, -3, -1}},
		{[]int{1, 2, 3, 4, 5}, 2, []int{2, 3, 4, 5}},
		{[]int{5, 4, 3, 2, 1}, 2, []int{5, 4, 3, 2}},
		{[]int{2, 1, 2, 1, 2}, 3, []int{2, 2, 2}},
	}
	for _, c := range cases {
		got := PeakPerWindow(c.readings, c.k)
		if len(got) == 0 && len(c.want) == 0 {
			if c.want == nil && got != nil {
				t.Errorf("PeakPerWindow(%v, %d) = %v, want nil", c.readings, c.k, got)
			}
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("PeakPerWindow(%v, %d) = %v, want %v", c.readings, c.k, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	readings := []int{3, 1, 4, 1, 5, 9, 2, 6}
	before := append([]int(nil), readings...)
	PeakPerWindow(readings, 3)
	if !reflect.DeepEqual(readings, before) {
		t.Fatalf("input modified: %v", readings)
	}
}

func brute(readings []int, k int) []int {
	if k < 1 || k > len(readings) {
		return nil
	}
	out := make([]int, 0, len(readings)-k+1)
	for w := 0; w+k <= len(readings); w++ {
		m := readings[w]
		for _, v := range readings[w+1 : w+k] {
			if v > m {
				m = v
			}
		}
		out = append(out, m)
	}
	return out
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(239))
	for round := 0; round < 300; round++ {
		n := rng.Intn(60) + 1
		readings := make([]int, n)
		spread := []int{3, 10, 1000}[round%3]
		for i := range readings {
			readings[i] = rng.Intn(spread) - spread/2
		}
		k := rng.Intn(n+2) - 1
		got, want := PeakPerWindow(readings, k), brute(readings, k)
		if (want == nil) != (got == nil) || !reflect.DeepEqual(got, want) && !(len(got) == 0 && len(want) == 0) {
			t.Fatalf("round %d: PeakPerWindow(%v, %d) = %v, want %v", round, readings, k, got, want)
		}
	}
}

func TestHiddenLargeInput(t *testing.T) {
	const n = 1_000_000
	const k = 100_000
	readings := make([]int, n)
	x := uint32(7)
	for i := range readings {
		x = x*1664525 + 1013904223
		readings[i] = int(x>>8) - (1 << 23)
	}
	for _, shape := range []struct {
		name string
		data []int
	}{
		{"random", readings},
		{"decreasing", decreasing(n)},
		{"increasing", increasing(n)},
	} {
		done := make(chan []int, 1)
		go func() { done <- PeakPerWindow(shape.data, k) }()
		var got []int
		select {
		case got = <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: PeakPerWindow took longer than 10s for n=%d k=%d", shape.name, n, k)
		}
		if len(got) != n-k+1 {
			t.Fatalf("%s: got %d peaks, want %d", shape.name, len(got), n-k+1)
		}
		for _, w := range []int{0, 1, 12345, n - k} {
			m := shape.data[w]
			for _, v := range shape.data[w+1 : w+k] {
				if v > m {
					m = v
				}
			}
			if got[w] != m {
				t.Fatalf("%s: window %d peak = %d, want %d", shape.name, w, got[w], m)
			}
		}
	}
}

func decreasing(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = n - i
	}
	return out
}

func increasing(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}
