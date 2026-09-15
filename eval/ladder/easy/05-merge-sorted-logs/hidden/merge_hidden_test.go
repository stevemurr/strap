package logs

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"
)

func sameEntries(got, want []Entry) bool {
	if len(got) == 0 && len(want) == 0 {
		return true
	}
	return reflect.DeepEqual(got, want)
}

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name string
		a, b []Entry
		want []Entry
	}{
		{"readme interleave", []Entry{{1, "boot"}, {4, "ready"}}, []Entry{{2, "probe"}, {3, "probe"}}, []Entry{{1, "boot"}, {2, "probe"}, {3, "probe"}, {4, "ready"}}},
		{"readme equal stamps", []Entry{{5, "a1"}}, []Entry{{5, "b1"}}, []Entry{{5, "a1"}, {5, "b1"}}},
		{"readme repeated stamps", []Entry{{1, "a1"}, {1, "a2"}}, []Entry{{0, "b1"}, {1, "b2"}}, []Entry{{0, "b1"}, {1, "a1"}, {1, "a2"}, {1, "b2"}}},
		{"readme a empty", nil, []Entry{{7, "x"}}, []Entry{{7, "x"}}},
		{"readme b empty", []Entry{{3, "x"}}, nil, []Entry{{3, "x"}}},
		{"readme both empty", nil, nil, nil},
		{"both empty non-nil", []Entry{}, []Entry{}, nil},
		{"a before b", []Entry{{1, "a"}, {2, "a"}}, []Entry{{3, "b"}, {4, "b"}}, []Entry{{1, "a"}, {2, "a"}, {3, "b"}, {4, "b"}}},
		{"b before a", []Entry{{3, "a"}, {4, "a"}}, []Entry{{1, "b"}, {2, "b"}}, []Entry{{1, "b"}, {2, "b"}, {3, "a"}, {4, "a"}}},
		{"negative", []Entry{{-5, "a"}, {0, "a"}}, []Entry{{-9, "b"}, {-1, "b"}}, []Entry{{-9, "b"}, {-5, "a"}, {-1, "b"}, {0, "a"}}},
		{"all equal", []Entry{{2, "a1"}, {2, "a2"}}, []Entry{{2, "b1"}, {2, "b2"}}, []Entry{{2, "a1"}, {2, "a2"}, {2, "b1"}, {2, "b2"}}},
		{"single each", []Entry{{9, "a"}}, []Entry{{8, "b"}}, []Entry{{8, "b"}, {9, "a"}}},
		{"b wins strictly", []Entry{{2, "a"}}, []Entry{{1, "b1"}, {2, "b2"}, {3, "b3"}}, []Entry{{1, "b1"}, {2, "a"}, {2, "b2"}, {3, "b3"}}},
		{"same line text", []Entry{{1, "x"}, {3, "x"}}, []Entry{{2, "x"}}, []Entry{{1, "x"}, {2, "x"}, {3, "x"}}},
	}
	for _, c := range cases {
		got := Merge(c.a, c.b)
		if !sameEntries(got, c.want) {
			t.Errorf("%s: Merge = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	a := []Entry{{1, "a1"}, {3, "a2"}, {3, "a3"}, {8, "a4"}}
	b := []Entry{{0, "b1"}, {3, "b2"}, {9, "b3"}}
	aBefore := append([]Entry(nil), a...)
	bBefore := append([]Entry(nil), b...)
	got := Merge(a, b)
	if len(got) != 7 {
		t.Fatalf("len = %d, want 7", len(got))
	}
	if !reflect.DeepEqual(a, aBefore) || !reflect.DeepEqual(b, bBefore) {
		t.Fatalf("inputs were modified: %v %v", a, b)
	}
}

func brute(a, b []Entry) []Entry {
	out := make([]Entry, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

func sortedRandom(rng *rand.Rand, prefix string, n int) []Entry {
	out := make([]Entry, n)
	at := int64(rng.Intn(5)) - 2
	for i := range out {
		at += int64(rng.Intn(3))
		out[i] = Entry{At: at, Line: fmt.Sprintf("%s%d", prefix, i)}
	}
	return out
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(21))
	for round := 0; round < 500; round++ {
		a := sortedRandom(rng, "a", rng.Intn(10))
		b := sortedRandom(rng, "b", rng.Intn(10))
		got, want := Merge(a, b), brute(a, b)
		if !sameEntries(got, want) {
			t.Fatalf("round %d: Merge(%v, %v) = %v, want %v", round, a, b, got, want)
		}
	}
}

func TestHiddenLargeStreams(t *testing.T) {
	const n = 1_000_000
	a := make([]Entry, n)
	b := make([]Entry, n)
	for i := range a {
		a[i] = Entry{At: int64(2 * i), Line: "a"}
		b[i] = Entry{At: int64(2*i + 1), Line: "b"}
	}
	run := func(name string) []Entry {
		done := make(chan []Entry, 1)
		go func() { done <- Merge(a, b) }()
		select {
		case got := <-done:
			return got
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: Merge took longer than 10s on %d entries each", name, n)
			return nil
		}
	}
	got := run("interleaved")
	if len(got) != 2*n {
		t.Fatalf("interleaved: len = %d, want %d", len(got), 2*n)
	}
	for i, e := range got {
		if e.At != int64(i) {
			t.Fatalf("interleaved: entry %d has At %d", i, e.At)
		}
	}
	for i := range a {
		a[i].At, b[i].At = 5, 5
	}
	got = run("equal stamps")
	if len(got) != 2*n {
		t.Fatalf("equal stamps: len = %d, want %d", len(got), 2*n)
	}
	for i, e := range got {
		want := "a"
		if i >= n {
			want = "b"
		}
		if e.At != 5 || e.Line != want {
			t.Fatalf("equal stamps: entry %d = %+v, want {5 %s}", i, e, want)
		}
	}
}
