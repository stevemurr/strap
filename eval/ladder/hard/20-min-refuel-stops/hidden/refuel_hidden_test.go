package roadtrip

import (
	"math/bits"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name      string
		target    int
		startFuel int
		stations  [][2]int
		want      int
	}{
		{"readme", 1, 1, nil, 0},
		{"unreachable first station", 100, 1, [][2]int{{10, 100}}, -1},
		{"two stops", 100, 10, [][2]int{{10, 60}, {20, 30}, {30, 30}, {60, 40}}, 2},
		{"reach a station on empty", 100, 50, [][2]int{{25, 25}, {50, 50}}, 1},
		{"chain on empty", 10, 3, [][2]int{{3, 3}, {6, 4}}, 2},
		{"empty stations", 10, 5, [][2]int{{2, 0}, {5, 0}}, -1},
		{"no stations short", 10, 9, nil, -1},
		{"no stations exact", 10, 10, [][2]int{}, 0},
		{"start beyond target", 10, 25, [][2]int{{5, 100}}, 0},
		{"zero start fuel", 5, 0, [][2]int{{1, 10}}, -1},
		{"skip small for big later", 100, 30, [][2]int{{10, 5}, {20, 5}, {30, 70}}, 1},
		{"big station too far", 100, 30, [][2]int{{10, 5}, {20, 5}, {41, 70}}, -1},
		{"small stop enables big one", 100, 20, [][2]int{{10, 5}, {20, 6}, {26, 80}}, 2},
		{"all stops needed", 4, 1, [][2]int{{1, 1}, {2, 1}, {3, 1}}, 3},
	}
	for _, c := range cases {
		if got := MinRefuelStops(c.target, c.startFuel, c.stations); got != c.want {
			t.Errorf("%s: MinRefuelStops(%d, %d, %v) = %d, want %d", c.name, c.target, c.startFuel, c.stations, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	stations := [][2]int{{10, 60}, {20, 30}, {30, 30}, {60, 40}}
	before := [][2]int{{10, 60}, {20, 30}, {30, 30}, {60, 40}}
	MinRefuelStops(100, 10, stations)
	if !reflect.DeepEqual(stations, before) {
		t.Fatalf("input modified: %v", stations)
	}
}

// hiddenBrute tries every subset of stations in road order.
func hiddenBrute(target, startFuel int, stations [][2]int) int {
	best := -1
	for mask := 0; mask < 1<<len(stations); mask++ {
		reach, ok := startFuel, true
		for i, s := range stations {
			if mask&(1<<i) == 0 {
				continue
			}
			if s[0] > reach {
				ok = false
				break
			}
			reach += s[1]
		}
		if ok && reach >= target {
			if stops := bits.OnesCount(uint(mask)); best < 0 || stops < best {
				best = stops
			}
		}
	}
	return best
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(871))
	for round := 0; round < 500; round++ {
		target := rng.Intn(40) + 2
		startFuel := rng.Intn(target)
		n := rng.Intn(min(10, target-1)) + 1
		positions := rng.Perm(target - 1)[:n]
		hiddenSortInts(positions)
		stations := make([][2]int, n)
		for i, p := range positions {
			stations[i] = [2]int{p + 1, rng.Intn(12)}
		}
		got, want := MinRefuelStops(target, startFuel, stations), hiddenBrute(target, startFuel, stations)
		if got != want {
			t.Fatalf("round %d: MinRefuelStops(%d, %d, %v) = %d, want %d", round, target, startFuel, stations, got, want)
		}
	}
}

func hiddenSortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

func TestHiddenLongHighway(t *testing.T) {
	const n = 500_000
	// Every thousandth station holds 1,000 units, the rest hold 1; with 1,000
	// units to start, reaching 500,001 needs 500 stops.
	sparse := make([][2]int, n)
	// Every station holds 2 units; starting with 1, reaching 500,001 takes a
	// stop at every other station.
	twos := make([][2]int, n)
	// Every station holds 1 unit; 1,000,000 is out of reach.
	ones := make([][2]int, n)
	for i := range sparse {
		pos := i + 1
		fuel := 1
		if pos%1000 == 0 {
			fuel = 1000
		}
		sparse[i] = [2]int{pos, fuel}
		twos[i] = [2]int{pos, 2}
		ones[i] = [2]int{pos, 1}
	}
	for _, shape := range []struct {
		name      string
		target    int
		startFuel int
		stations  [][2]int
		want      int
	}{
		{"sparse big stations", 500_001, 1000, sparse, 500},
		{"every other station", 500_001, 1, twos, 250_000},
		{"unreachable", 1_000_000, 1000, ones, -1},
	} {
		done := make(chan int, 1)
		go func() { done <- MinRefuelStops(shape.target, shape.startFuel, shape.stations) }()
		select {
		case got := <-done:
			if got != shape.want {
				t.Fatalf("%s: got %d, want %d", shape.name, got, shape.want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: MinRefuelStops took longer than 10s on %d stations", shape.name, n)
		}
	}
}
