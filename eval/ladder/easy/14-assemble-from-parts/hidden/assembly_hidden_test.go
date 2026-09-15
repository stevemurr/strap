package assembly

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name              string
		needed, available []string
		want              bool
	}{
		{"readme", []string{"bolt", "bolt", "nut"}, []string{"nut", "bolt", "washer", "bolt"}, true},
		{"one bolt short", []string{"bolt", "bolt"}, []string{"bolt", "nut"}, false},
		{"empty bin", []string{"gear"}, nil, false},
		{"nothing needed", nil, nil, true},
		{"nothing needed full bin", nil, []string{"gear", "nut"}, true},
		{"case sensitive", []string{"Bolt"}, []string{"bolt"}, false},
		{"exact match", []string{"a", "b", "c"}, []string{"c", "b", "a"}, true},
		{"missing part", []string{"a", "b", "d"}, []string{"a", "b", "c"}, false},
		{"needs more than bin holds", []string{"a", "a", "a"}, []string{"a", "a"}, false},
		{"extras ignored", []string{"a"}, []string{"b", "a", "b", "b"}, true},
		{"classic false", []string{"a", "a"}, []string{"a", "b"}, false},
		{"classic true", []string{"a", "a"}, []string{"a", "a", "b"}, true},
		{"empty names", []string{"", ""}, []string{"", "x", ""}, true},
		{"more needed than available", []string{"x", "y"}, []string{"x"}, false},
	}
	for _, c := range cases {
		if got := CanAssemble(c.needed, c.available); got != c.want {
			t.Errorf("%s: CanAssemble(%q, %q) = %v, want %v", c.name, c.needed, c.available, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	needed := []string{"nut", "bolt", "nut"}
	available := []string{"bolt", "nut", "washer", "nut"}
	nBefore := append([]string(nil), needed...)
	aBefore := append([]string(nil), available...)
	CanAssemble(needed, available)
	if !reflect.DeepEqual(needed, nBefore) || !reflect.DeepEqual(available, aBefore) {
		t.Fatalf("inputs modified: needed=%q available=%q", needed, available)
	}
}

func brute(needed, available []string) bool {
	used := make([]bool, len(available))
	for _, part := range needed {
		found := false
		for j, have := range available {
			if !used[j] && have == part {
				used[j] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(383))
	parts := []string{"bolt", "nut", "gear", "Bolt", ""}
	draw := func(n, distinct int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = parts[rng.Intn(distinct)]
		}
		return out
	}
	for round := 0; round < 1000; round++ {
		distinct := 1 + rng.Intn(len(parts))
		needed := draw(rng.Intn(8), distinct)
		available := draw(rng.Intn(12), distinct)
		if got, want := CanAssemble(needed, available), brute(needed, available); got != want {
			t.Fatalf("round %d: CanAssemble(%q, %q) = %v, want %v", round, needed, available, got, want)
		}
	}
}

func TestHiddenLargeBin(t *testing.T) {
	const n = 1_000_000
	const distinct = 1000
	names := make([]string, distinct)
	for i := range names {
		names[i] = fmt.Sprintf("part-%04d", i)
	}
	x := uint32(383)
	needed := make([]string, n)
	for i := range needed {
		x = x*1664525 + 1013904223
		needed[i] = names[(x>>8)%distinct]
	}
	// The same parts in reverse order: assemblable with nothing to spare.
	exact := make([]string, n)
	for i := range exact {
		exact[i] = needed[n-1-i]
	}
	// Swap one unit for a different part: some part is now one short.
	short := append([]string(nil), exact...)
	if short[0] == names[0] {
		short[0] = names[1]
	} else {
		short[0] = names[0]
	}
	run := func(name string, available []string, want bool) {
		done := make(chan bool, 1)
		go func() { done <- CanAssemble(needed, available) }()
		select {
		case got := <-done:
			if got != want {
				t.Fatalf("%s: CanAssemble on %d needed and %d available = %v, want %v", name, n, len(available), got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: CanAssemble took longer than 10s on %d + %d entries", name, n, len(available))
		}
	}
	run("exact", exact, true)
	run("short", short, false)
}
