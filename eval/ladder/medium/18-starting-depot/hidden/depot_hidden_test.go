package route

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name       string
		fuel, cost []int
		want       int
	}{
		{"readme classic", []int{1, 2, 3, 4, 5}, []int{3, 4, 5, 1, 2}, 3},
		{"readme impossible", []int{2, 3, 4}, []int{3, 4, 3}, -1},
		{"readme last depot", []int{5, 1, 2, 3, 4}, []int{4, 4, 1, 5, 1}, 4},
		{"readme single ok", []int{3}, []int{3}, 0},
		{"readme single short", []int{2}, []int{3}, -1},
		{"readme break even tie", []int{1, 1}, []int{1, 1}, 0},
		{"zero everywhere", []int{0, 0, 0}, []int{0, 0, 0}, 0},
		{"first depot", []int{4, 0, 0}, []int{1, 1, 1}, 0},
		{"middle depot", []int{0, 4, 0}, []int{1, 1, 1}, 1},
		{"exactly enough overall but wrong order", []int{0, 3}, []int{1, 2}, 1},
		{"one short overall", []int{1, 1, 1}, []int{1, 1, 2}, -1},
		{"tie picks smallest", []int{2, 0, 2, 0}, []int{1, 1, 1, 1}, 0},
		{"tie later", []int{0, 2, 0, 2}, []int{1, 1, 1, 1}, 1},
		{"needs wraparound", []int{0, 0, 5}, []int{2, 2, 1}, 2},
	}
	for _, c := range cases {
		if got := StartingDepot(c.fuel, c.cost); got != c.want {
			t.Errorf("%s: StartingDepot(%v, %v) = %d, want %d", c.name, c.fuel, c.cost, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	fuel := []int{1, 2, 3, 4, 5}
	cost := []int{3, 4, 5, 1, 2}
	fuelBefore := append([]int(nil), fuel...)
	costBefore := append([]int(nil), cost...)
	StartingDepot(fuel, cost)
	if !reflect.DeepEqual(fuel, fuelBefore) || !reflect.DeepEqual(cost, costBefore) {
		t.Fatalf("inputs modified: %v %v", fuel, cost)
	}
}

func brute(fuel, cost []int) int {
	n := len(fuel)
	for s := 0; s < n; s++ {
		tank := 0
		ok := true
		for k := 0; k < n; k++ {
			i := (s + k) % n
			tank += fuel[i] - cost[i]
			if tank < 0 {
				ok = false
				break
			}
		}
		if ok {
			return s
		}
	}
	return -1
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(134))
	for round := 0; round < 2000; round++ {
		n := rng.Intn(12) + 1
		spread := []int{2, 3, 6, 50}[round%4]
		fuel := make([]int, n)
		cost := make([]int, n)
		for i := range fuel {
			fuel[i] = rng.Intn(spread)
			cost[i] = rng.Intn(spread)
		}
		got, want := StartingDepot(fuel, cost), brute(fuel, cost)
		if got != want {
			t.Fatalf("round %d: StartingDepot(%v, %v) = %d, want %d", round, fuel, cost, got, want)
		}
	}
}

func TestHiddenLongRoute(t *testing.T) {
	const n = 1_000_000
	run := func(name string, fuel, cost []int) int {
		done := make(chan int, 1)
		go func() { done <- StartingDepot(fuel, cost) }()
		select {
		case got := <-done:
			return got
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: StartingDepot took longer than 10s on %d depots", name, n)
			return 0
		}
	}

	// m legs gaining 1, then k legs losing 2, then one final depot with a
	// big pickup: every early start runs dry deep inside the losing stretch,
	// and only the last depot completes the loop (with exactly zero to spare).
	const m, k = 600_000, 399_999
	fuel := make([]int, n)
	cost := make([]int, n)
	for i := 0; i < m; i++ {
		fuel[i], cost[i] = 2, 1
	}
	for i := m; i < m+k; i++ {
		fuel[i], cost[i] = 0, 2
	}
	fuel[n-1], cost[n-1] = 2*k-m, 0
	if got := run("last depot", fuel, cost); got != n-1 {
		t.Fatalf("last depot: got %d, want %d", got, n-1)
	}

	// Half the legs gain 1, half lose 2: nobody makes it round.
	for i := range fuel {
		if i < n/2 {
			fuel[i], cost[i] = 2, 1
		} else {
			fuel[i], cost[i] = 0, 2
		}
	}
	if got := run("impossible", fuel, cost); got != -1 {
		t.Fatalf("impossible: got %d, want -1", got)
	}
}
