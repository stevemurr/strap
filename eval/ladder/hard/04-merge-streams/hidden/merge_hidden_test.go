package feeds

import (
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"
)

func hiddenSameInts(got, want []int) bool {
	if len(got) == 0 && len(want) == 0 {
		return true
	}
	return reflect.DeepEqual(got, want)
}

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name    string
		streams [][]int
		want    []int
	}{
		{"readme", [][]int{{1, 4, 5}, {1, 3, 4}, {2, 6}}, []int{1, 1, 2, 3, 4, 4, 5, 6}},
		{"negatives and empty", [][]int{{-3, -1}, {-2, 0}, {}}, []int{-3, -2, -1, 0}},
		{"single element", [][]int{{7}}, []int{7}},
		{"duplicates", [][]int{{1, 1}, {1}}, []int{1, 1, 1}},
		{"all empty", [][]int{{}, {}}, nil},
		{"no streams", nil, nil},
		{"nil stream", [][]int{nil}, nil},
		{"one stream", [][]int{{1, 2, 2, 3}}, []int{1, 2, 2, 3}},
		{"disjoint ranges", [][]int{{10, 11}, {1, 2}, {5, 6}}, []int{1, 2, 5, 6, 10, 11}},
		{"interleaved", [][]int{{1, 3, 5}, {2, 4, 6}}, []int{1, 2, 3, 4, 5, 6}},
		{"uneven lengths", [][]int{{9}, {1, 2, 3, 4, 5}, {}, {0}}, []int{0, 1, 2, 3, 4, 5, 9}},
		{"all equal", [][]int{{2, 2}, {2}, {2, 2, 2}}, []int{2, 2, 2, 2, 2, 2}},
	}
	for _, c := range cases {
		got := Merge(c.streams)
		if !hiddenSameInts(got, c.want) {
			t.Errorf("%s: Merge(%v) = %v, want %v", c.name, c.streams, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutateOrAlias(t *testing.T) {
	streams := [][]int{{3, 5, 9}, {1, 4}, {2}}
	before := [][]int{{3, 5, 9}, {1, 4}, {2}}
	got := Merge(streams)
	if !reflect.DeepEqual(streams, before) {
		t.Fatalf("input modified: %v", streams)
	}
	for i := range got {
		got[i] = -100
	}
	if !reflect.DeepEqual(streams, before) {
		t.Fatalf("result shares memory with the input: %v", streams)
	}
	single := [][]int{{1, 2, 3}}
	out := Merge(single)
	if len(out) != 3 {
		t.Fatalf("single stream: got %v", out)
	}
	out[0] = 42
	if single[0][0] != 1 {
		t.Fatalf("result aliases the only input stream: %v", single[0])
	}
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(23))
	for round := 0; round < 300; round++ {
		k := rng.Intn(8)
		streams := make([][]int, k)
		var all []int
		for i := range streams {
			n := rng.Intn(7)
			s := make([]int, n)
			for j := range s {
				s[j] = rng.Intn(21) - 10
			}
			sort.Ints(s)
			streams[i] = s
			all = append(all, s...)
		}
		sort.Ints(all)
		if got := Merge(streams); !hiddenSameInts(got, all) {
			t.Fatalf("round %d: Merge(%v) = %v, want %v", round, streams, got, all)
		}
	}
}

func TestHiddenManyStreams(t *testing.T) {
	const k = 20_000
	const per = 50
	streams := make([][]int, k)
	all := make([]int, 0, k*per)
	x := uint32(5)
	for i := range streams {
		s := make([]int, per)
		for j := range s {
			x = x*1664525 + 1013904223
			s[j] = int(x>>8) - (1 << 23)
		}
		sort.Ints(s)
		streams[i] = s
		all = append(all, s...)
	}
	sort.Ints(all)
	done := make(chan []int, 1)
	go func() { done <- Merge(streams) }()
	var got []int
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("Merge took longer than 10s on %d streams of %d elements", k, per)
	}
	if len(got) != len(all) {
		t.Fatalf("got %d elements, want %d", len(got), len(all))
	}
	for i := range got {
		if got[i] != all[i] {
			t.Fatalf("element %d = %d, want %d", i, got[i], all[i])
		}
	}
}
