package reconcile

import (
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"
)

func hiddenSameTriples(got, want [][3]int) bool {
	if len(got) == 0 && len(want) == 0 {
		return true
	}
	return reflect.DeepEqual(got, want)
}

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name   string
		deltas []int
		want   [][3]int
	}{
		{"readme classic", []int{-1, 0, 1, 2, -1, -4}, [][3]int{{-1, -1, 2}, {-1, 0, 1}}},
		{"readme zeros", []int{0, 0, 0, 0}, [][3]int{{0, 0, 0}}},
		{"readme two zeros", []int{0, 0}, nil},
		{"readme duplicates", []int{3, -2, -1, -2, 4}, [][3]int{{-2, -2, 4}, {-2, -1, 3}}},
		{"readme positives", []int{1, 2, 3}, nil},
		{"readme repeated pair", []int{-5, 5, 0, 5, -5}, [][3]int{{-5, 0, 5}}},
		{"empty", nil, nil},
		{"single", []int{0}, nil},
		{"exactly three zeros", []int{0, 0, 0}, [][3]int{{0, 0, 0}}},
		{"needs two of the same", []int{-1, 2}, nil},
		{"two of the same once", []int{-1, -1, 2}, [][3]int{{-1, -1, 2}}},
		{"one negative many pairs", []int{-3, 1, 2, 0, 3, -2, -1}, [][3]int{{-3, 0, 3}, {-3, 1, 2}, {-2, -1, 3}, {-2, 0, 2}, {-1, 0, 1}}},
		{"large magnitudes", []int{-1000000, 1000000, 0, 999999, 1}, [][3]int{{-1000000, 0, 1000000}, {-1000000, 1, 999999}}},
		{"all negative", []int{-1, -2, -3}, nil},
	}
	for _, c := range cases {
		if got := ZeroSumTriples(c.deltas); !hiddenSameTriples(got, c.want) {
			t.Errorf("%s: ZeroSumTriples(%v) = %v, want %v", c.name, c.deltas, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	deltas := []int{2, -1, 0, -1, 3, -3}
	before := append([]int(nil), deltas...)
	ZeroSumTriples(deltas)
	if !reflect.DeepEqual(deltas, before) {
		t.Fatalf("input modified: %v", deltas)
	}
}

func hiddenSortTriples(s [][3]int) {
	sort.Slice(s, func(i, j int) bool {
		if s[i][0] != s[j][0] {
			return s[i][0] < s[j][0]
		}
		if s[i][1] != s[j][1] {
			return s[i][1] < s[j][1]
		}
		return s[i][2] < s[j][2]
	})
}

func hiddenBrute(deltas []int) [][3]int {
	seen := map[[3]int]bool{}
	for i := range deltas {
		for j := i + 1; j < len(deltas); j++ {
			for k := j + 1; k < len(deltas); k++ {
				if deltas[i]+deltas[j]+deltas[k] != 0 {
					continue
				}
				v := []int{deltas[i], deltas[j], deltas[k]}
				sort.Ints(v)
				seen[[3]int{v[0], v[1], v[2]}] = true
			}
		}
	}
	out := make([][3]int, 0, len(seen))
	for tr := range seen {
		out = append(out, tr)
	}
	hiddenSortTriples(out)
	return out
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(15))
	for round := 0; round < 500; round++ {
		n := rng.Intn(14)
		spread := []int{3, 7, 21, 1000}[round%4]
		deltas := make([]int, n)
		for i := range deltas {
			deltas[i] = rng.Intn(spread) - spread/2
		}
		got, want := ZeroSumTriples(deltas), hiddenBrute(deltas)
		if !hiddenSameTriples(got, want) {
			t.Fatalf("round %d: ZeroSumTriples(%v) = %v, want %v", round, deltas, got, want)
		}
	}
}

// hiddenCounting enumerates distinct value pairs (a <= b) and looks up c = -a-b,
// checking that the multiset holds enough copies of each value.
func hiddenCounting(deltas []int) [][3]int {
	count := map[int]int{}
	for _, v := range deltas {
		count[v]++
	}
	values := make([]int, 0, len(count))
	for v := range count {
		values = append(values, v)
	}
	sort.Ints(values)
	var out [][3]int
	for i, a := range values {
		for _, b := range values[i:] {
			c := -a - b
			if c < b {
				break
			}
			ok := false
			switch {
			case a == b && b == c:
				ok = count[a] >= 3
			case a == b:
				ok = count[a] >= 2 && count[c] >= 1
			case b == c:
				ok = count[b] >= 2
			default:
				ok = count[c] >= 1
			}
			if ok {
				out = append(out, [3]int{a, b, c})
			}
		}
	}
	return out
}

func TestHiddenLargeInputs(t *testing.T) {
	const n = 6000
	run := func(name string, deltas []int) [][3]int {
		done := make(chan [][3]int, 1)
		go func() { done <- ZeroSumTriples(deltas) }()
		select {
		case got := <-done:
			return got
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: ZeroSumTriples took longer than 10s on %d deltas", name, n)
			return nil
		}
	}
	compare := func(name string, got, want [][3]int) {
		if len(got) != len(want) {
			t.Fatalf("%s: got %d triples, want %d", name, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: triple %d = %v, want %v", name, i, got[i], want[i])
			}
		}
	}
	rng := rand.New(rand.NewSource(150))
	for _, shape := range []struct {
		name   string
		spread int
	}{
		{"narrow", 2001},
		{"wide", 2_000_001},
	} {
		deltas := make([]int, n)
		for i := range deltas {
			deltas[i] = rng.Intn(shape.spread) - shape.spread/2
		}
		before := append([]int(nil), deltas...)
		got := run(shape.name, deltas)
		if !reflect.DeepEqual(deltas, before) {
			t.Fatalf("%s: input modified", shape.name)
		}
		compare(shape.name, got, hiddenCounting(deltas))
	}
}
