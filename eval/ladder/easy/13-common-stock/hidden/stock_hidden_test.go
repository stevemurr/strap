package inventory

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want []string
	}{
		{"readme", []string{"A1", "B2", "A1"}, []string{"A1", "A1", "C3"}, []string{"A1", "A1"}},
		{"sorted output", []string{"X9", "Y1"}, []string{"Y1", "X9"}, []string{"X9", "Y1"}},
		{"min count", []string{"Z", "Z", "Z"}, []string{"Z"}, []string{"Z"}},
		{"min count other side", []string{"Z"}, []string{"Z", "Z", "Z"}, []string{"Z"}},
		{"disjoint", []string{"A1"}, []string{"B2"}, nil},
		{"empty a", nil, []string{"A1"}, nil},
		{"empty b", []string{"A1"}, nil, nil},
		{"both empty", nil, nil, nil},
		{"case sensitive", []string{"a1"}, []string{"A1"}, nil},
		{"single match", []string{"K"}, []string{"K"}, []string{"K"}},
		{"classic", []string{"4", "9", "5"}, []string{"9", "4", "9", "8", "4"}, []string{"4", "9"}},
		{"empty sku", []string{"", "A"}, []string{"A", ""}, []string{"", "A"}},
		{"byte order", []string{"b", "B", "a", "A"}, []string{"A", "a", "B", "b"}, []string{"A", "B", "a", "b"}},
		{"mixed counts", []string{"p", "q", "p", "q", "p"}, []string{"q", "p", "q", "q"}, []string{"p", "q", "q"}},
	}
	for _, c := range cases {
		got := CommonStock(c.a, c.b)
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: CommonStock(%q, %q) = %q, want %q", c.name, c.a, c.b, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	a := []string{"C", "A", "B", "A"}
	b := []string{"B", "A", "D"}
	aBefore := append([]string(nil), a...)
	bBefore := append([]string(nil), b...)
	CommonStock(a, b)
	if !reflect.DeepEqual(a, aBefore) || !reflect.DeepEqual(b, bBefore) {
		t.Fatalf("inputs modified: a=%q b=%q", a, b)
	}
}

func brute(a, b []string) []string {
	used := make([]bool, len(b))
	var out []string
	for _, sku := range a {
		for j, other := range b {
			if !used[j] && other == sku {
				used[j] = true
				out = append(out, sku)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(350))
	skus := []string{"A", "B", "C", "D", "E", "a", "b", ""}
	draw := func(n, distinct int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = skus[rng.Intn(distinct)]
		}
		return out
	}
	for round := 0; round < 1000; round++ {
		distinct := 1 + rng.Intn(len(skus))
		a := draw(rng.Intn(12), distinct)
		b := draw(rng.Intn(12), distinct)
		got, want := CommonStock(a, b), brute(a, b)
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round %d: CommonStock(%q, %q) = %q, want %q", round, a, b, got, want)
		}
	}
}

func TestHiddenLargeInventories(t *testing.T) {
	const n = 500_000
	const distinct = 200_000
	names := make([]string, distinct)
	for i := range names {
		names[i] = fmt.Sprintf("SKU-%06d", i)
	}
	x := uint32(350)
	next := func() uint32 {
		x = x*1664525 + 1013904223
		return x >> 8
	}
	a := make([]string, n)
	b := make([]string, n)
	for i := range a {
		a[i] = names[next()%distinct]
		b[i] = names[next()%distinct]
	}
	countA := make(map[string]int, distinct)
	for _, s := range a {
		countA[s]++
	}
	countB := make(map[string]int, distinct)
	for _, s := range b {
		countB[s]++
	}
	wantCount := func(s string) int {
		if countB[s] < countA[s] {
			return countB[s]
		}
		return countA[s]
	}
	wantLen := 0
	for s := range countA {
		wantLen += wantCount(s)
	}
	done := make(chan []string, 1)
	go func() { done <- CommonStock(a, b) }()
	var got []string
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("CommonStock took longer than 10s on %d + %d entries", n, n)
	}
	if len(got) != wantLen {
		t.Fatalf("got %d common units, want %d", len(got), wantLen)
	}
	if !sort.StringsAreSorted(got) {
		t.Fatal("result is not sorted")
	}
	gotCount := make(map[string]int, distinct)
	for _, s := range got {
		gotCount[s]++
	}
	for _, s := range []string{names[0], names[12345], names[distinct-1]} {
		if gotCount[s] != wantCount(s) {
			t.Fatalf("%s appears %d times, want %d", s, gotCount[s], wantCount(s))
		}
	}
}
