package calendar

import (
	"math/rand"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name    string
		periods [][2]int
		want    [][2]int
	}{
		{"readme", [][2]int{{8, 10}, {1, 3}, {2, 6}, {15, 18}}, [][2]int{{1, 6}, {8, 10}, {15, 18}}},
		{"touching merges", [][2]int{{1, 4}, {4, 5}}, [][2]int{{1, 5}}},
		{"adjacent minutes stay apart", [][2]int{{1, 10}, {11, 20}}, [][2]int{{1, 10}, {11, 20}}},
		{"chain", [][2]int{{1, 3}, {2, 4}, {3, 5}}, [][2]int{{1, 5}}},
		{"negative", [][2]int{{-5, -1}, {-3, 2}, {7, 7}}, [][2]int{{-5, 2}, {7, 7}}},
		{"contained", [][2]int{{1, 10}, {2, 3}}, [][2]int{{1, 10}}},
		{"single", [][2]int{{4, 9}}, [][2]int{{4, 9}}},
		{"instant", [][2]int{{5, 5}}, [][2]int{{5, 5}}},
		{"instant touches", [][2]int{{5, 5}, {5, 8}, {8, 8}}, [][2]int{{5, 8}}},
		{"duplicates", [][2]int{{3, 6}, {3, 6}, {3, 6}}, [][2]int{{3, 6}}},
		{"reverse order", [][2]int{{30, 40}, {20, 25}, {10, 15}}, [][2]int{{10, 15}, {20, 25}, {30, 40}}},
		{"same start different ends", [][2]int{{1, 2}, {1, 9}, {1, 5}}, [][2]int{{1, 9}}},
		{"later short period inside earlier long one", [][2]int{{5, 6}, {0, 100}, {50, 51}}, [][2]int{{0, 100}}},
	}
	for _, c := range cases {
		got := MergeBusy(c.periods)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: MergeBusy(%v) = %v, want %v", c.name, c.periods, got, c.want)
		}
	}
}

func TestHiddenEmpty(t *testing.T) {
	if got := MergeBusy(nil); len(got) != 0 {
		t.Fatalf("MergeBusy(nil) = %v, want empty", got)
	}
	if got := MergeBusy([][2]int{}); len(got) != 0 {
		t.Fatalf("MergeBusy([]) = %v, want empty", got)
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	periods := [][2]int{{8, 10}, {1, 3}, {2, 6}, {15, 18}, {-4, 0}}
	before := slices.Clone(periods)
	MergeBusy(periods)
	if !slices.Equal(periods, before) {
		t.Fatalf("input modified: %v", periods)
	}
}

// hiddenBruteMerge marks every covered half-minute so that touching periods share a
// point while periods one minute apart leave a gap, then reads off the runs.
func hiddenBruteMerge(periods [][2]int) [][2]int {
	if len(periods) == 0 {
		return nil
	}
	lo, hi := periods[0][0], periods[0][1]
	for _, p := range periods {
		lo, hi = min(lo, p[0]), max(hi, p[1])
	}
	covered := make([]bool, 2*(hi-lo)+1)
	for _, p := range periods {
		for x := 2 * (p[0] - lo); x <= 2*(p[1]-lo); x++ {
			covered[x] = true
		}
	}
	var out [][2]int
	for x := 0; x < len(covered); {
		if !covered[x] {
			x++
			continue
		}
		start := x
		for x < len(covered) && covered[x] {
			x++
		}
		out = append(out, [2]int{lo + start/2, lo + (x-1)/2})
	}
	return out
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(56))
	for round := 0; round < 500; round++ {
		n := rng.Intn(14)
		periods := make([][2]int, n)
		for i := range periods {
			start := rng.Intn(40) - 10
			periods[i] = [2]int{start, start + rng.Intn(7)}
		}
		got, want := MergeBusy(periods), hiddenBruteMerge(periods)
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round %d: MergeBusy(%v) = %v, want %v", round, periods, got, want)
		}
	}
}

func TestHiddenLargeInput(t *testing.T) {
	const n = 1_000_000
	rng := rand.New(rand.NewSource(5656))
	// Build disjoint blocks, each cut into overlapping, touching or contained
	// pieces, so the expected answer is known by construction.
	var want [][2]int
	periods := make([][2]int, 0, n)
	pos := -500_000_000
	for len(periods) < n {
		pieces := min(1+rng.Intn(8), n-len(periods))
		start := pos
		end := start + rng.Intn(60)
		periods = append(periods, [2]int{start, end})
		for p := 1; p < pieces; p++ {
			s := start + rng.Intn(end-start+1)
			e := s + rng.Intn(60)
			periods = append(periods, [2]int{s, e})
			end = max(end, e)
		}
		want = append(want, [2]int{start, end})
		pos = end + 1 + rng.Intn(1000)
	}
	rng.Shuffle(len(periods), func(i, j int) { periods[i], periods[j] = periods[j], periods[i] })
	before := slices.Clone(periods)

	done := make(chan [][2]int, 1)
	go func() { done <- MergeBusy(periods) }()
	var got [][2]int
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("MergeBusy took longer than 10s on %d periods", n)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d blocks, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("block %d = %v, want %v", i, got[i], want[i])
		}
	}
	if !slices.Equal(periods, before) {
		t.Fatal("input modified")
	}
}
