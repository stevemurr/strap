package traffic

import (
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name string
		hits []string
		k    int
		want []string
	}{
		{"readme", []string{"/a", "/b", "/a", "/c", "/b", "/a"}, 2, []string{"/a", "/b"}},
		{"most frequent first", []string{"/p", "/q", "/q", "/r", "/r", "/r"}, 2, []string{"/r", "/q"}},
		{"tie by name", []string{"/x", "/y", "/y", "/x"}, 1, []string{"/x"}},
		{"k larger than distinct", []string{"/a", "/b"}, 5, []string{"/a", "/b"}},
		{"k equals distinct", []string{"/b", "/a", "/b"}, 2, []string{"/b", "/a"}},
		{"single hit", []string{"/only"}, 1, []string{"/only"}},
		{"trailing slash differs", []string{"/a/", "/a", "/a/"}, 2, []string{"/a/", "/a"}},
		{"mixed ties", []string{"/c", "/b", "/a", "/c", "/b", "/d"}, 3, []string{"/b", "/c", "/a"}},
		{"all ties", []string{"/z", "/y", "/x"}, 2, []string{"/x", "/y"}},
	}
	for _, c := range cases {
		got := TopEndpoints(c.hits, c.k)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: TopEndpoints(%q, %d) = %q, want %q", c.name, c.hits, c.k, got, c.want)
		}
	}
}

func TestHiddenEmptyResults(t *testing.T) {
	for _, c := range []struct {
		hits []string
		k    int
	}{
		{[]string{"/a"}, 0},
		{[]string{"/a", "/b"}, -3},
		{nil, 3},
		{[]string{}, 1},
		{nil, 0},
	} {
		if got := TopEndpoints(c.hits, c.k); len(got) != 0 {
			t.Errorf("TopEndpoints(%q, %d) = %q, want empty", c.hits, c.k, got)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	hits := []string{"/c", "/b", "/a", "/c", "/b", "/c"}
	before := slices.Clone(hits)
	TopEndpoints(hits, 2)
	if !slices.Equal(hits, before) {
		t.Fatalf("input modified: %q", hits)
	}
}

// hiddenBruteTop counts every distinct endpoint by rescanning the log and picks the
// winners by repeated selection.
func hiddenBruteTop(hits []string, k int) []string {
	if k <= 0 {
		return nil
	}
	var distinct []string
	for _, h := range hits {
		if !slices.Contains(distinct, h) {
			distinct = append(distinct, h)
		}
	}
	count := func(e string) int {
		n := 0
		for _, h := range hits {
			if h == e {
				n++
			}
		}
		return n
	}
	var out []string
	for len(out) < k && len(distinct) > 0 {
		best := 0
		for i := 1; i < len(distinct); i++ {
			ci, cb := count(distinct[i]), count(distinct[best])
			if ci > cb || ci == cb && distinct[i] < distinct[best] {
				best = i
			}
		}
		out = append(out, distinct[best])
		distinct = slices.Delete(distinct, best, best+1)
	}
	return out
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(347))
	names := []string{"/a", "/b", "/c", "/d", "/e", "/f", "/g", "/a/", "/A"}
	for round := 0; round < 500; round++ {
		n := rng.Intn(40)
		pool := names[:1+rng.Intn(len(names))]
		hits := make([]string, n)
		for i := range hits {
			hits[i] = pool[rng.Intn(len(pool))]
		}
		k := rng.Intn(len(names)+2) - 1
		got, want := TopEndpoints(hits, k), hiddenBruteTop(hits, k)
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round %d: TopEndpoints(%q, %d) = %q, want %q", round, hits, k, got, want)
		}
	}
}

func TestHiddenLargeLog(t *testing.T) {
	const distinct = 50_000
	const k = 100
	names := make([]string, distinct)
	for i := range names {
		names[i] = fmt.Sprintf("/api/v1/resource/%05d", i)
	}
	rng := rand.New(rand.NewSource(34747))

	// Skewed: the last 200 endpoints get extra hits, so the top 100 are the
	// highest-numbered names in descending order despite their names sorting
	// last. Flat: every endpoint has 20 hits, so the tie-break decides.
	skewed := make([]string, 0, distinct*20+20_100)
	flat := make([]string, 0, distinct*20)
	for i, name := range names {
		extra := 0
		if i >= distinct-200 {
			extra = i - (distinct - 200) + 1
		}
		for j := 0; j < 20+extra; j++ {
			skewed = append(skewed, name)
		}
		for j := 0; j < 20; j++ {
			flat = append(flat, name)
		}
	}
	rng.Shuffle(len(skewed), func(i, j int) { skewed[i], skewed[j] = skewed[j], skewed[i] })
	rng.Shuffle(len(flat), func(i, j int) { flat[i], flat[j] = flat[j], flat[i] })
	wantSkewed := make([]string, k)
	for i := range wantSkewed {
		wantSkewed[i] = names[distinct-1-i]
	}
	wantFlat := names[:k]

	for _, shape := range []struct {
		name string
		hits []string
		want []string
	}{
		{"skewed", skewed, wantSkewed},
		{"flat", flat, wantFlat},
	} {
		done := make(chan []string, 1)
		go func() { done <- TopEndpoints(shape.hits, k) }()
		var got []string
		select {
		case got = <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: TopEndpoints took longer than 10s on %d hits", shape.name, len(shape.hits))
		}
		if len(got) != k {
			t.Fatalf("%s: got %d endpoints, want %d", shape.name, len(got), k)
		}
		for i := range got {
			if got[i] != shape.want[i] {
				t.Fatalf("%s: result[%d] = %q, want %q", shape.name, i, got[i], shape.want[i])
			}
		}
	}
}
