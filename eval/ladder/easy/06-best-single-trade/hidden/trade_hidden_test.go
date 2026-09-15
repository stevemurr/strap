package trading

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

type hiddenTrade struct{ buy, sell, profit int }

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name   string
		prices []int
		want   hiddenTrade
	}{
		{"readme classic", []int{7, 1, 5, 3, 6, 4}, hiddenTrade{1, 4, 5}},
		{"readme falling", []int{7, 6, 4, 3, 1}, hiddenTrade{0, 0, 0}},
		{"readme tie on buy", []int{2, 4, 1, 3}, hiddenTrade{0, 1, 2}},
		{"readme tie on both", []int{1, 5, 1, 5}, hiddenTrade{0, 1, 4}},
		{"readme flat", []int{3, 3, 3}, hiddenTrade{0, 0, 0}},
		{"readme empty", nil, hiddenTrade{0, 0, 0}},
		{"single", []int{9}, hiddenTrade{0, 0, 0}},
		{"two rising", []int{1, 2}, hiddenTrade{0, 1, 1}},
		{"two falling", []int{2, 1}, hiddenTrade{0, 0, 0}},
		{"two equal", []int{4, 4}, hiddenTrade{0, 0, 0}},
		{"negative prices", []int{-5, -1}, hiddenTrade{0, 1, 4}},
		{"across zero", []int{-2, 0, 3}, hiddenTrade{0, 2, 5}},
		{"zero then rise", []int{0, 0, 1}, hiddenTrade{0, 2, 1}},
		{"later low ties earlier", []int{3, 1, 4, 1, 5}, hiddenTrade{1, 4, 4}},
		{"tie on sell", []int{5, 1, 5, 1, 5}, hiddenTrade{1, 2, 4}},
		{"best is not from global min", []int{2, 10, 1, 5}, hiddenTrade{0, 1, 8}},
		{"best is from global min", []int{2, 4, 1, 9}, hiddenTrade{2, 3, 8}},
		{"rise at end", []int{5, 4, 3, 2, 1, 6}, hiddenTrade{4, 5, 5}},
		{"peak before trough", []int{1, 9, 0, 7}, hiddenTrade{0, 1, 8}},
	}
	for _, c := range cases {
		buy, sell, profit := BestTrade(c.prices)
		if got := (hiddenTrade{buy, sell, profit}); got != c.want {
			t.Errorf("%s: BestTrade(%v) = %+v, want %+v", c.name, c.prices, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	prices := []int{9, 2, 7, 1, 8}
	before := append([]int(nil), prices...)
	BestTrade(prices)
	if !reflect.DeepEqual(prices, before) {
		t.Fatalf("input was modified: %v", prices)
	}
}

func hiddenBrute(prices []int) hiddenTrade {
	best := hiddenTrade{}
	for i := range prices {
		for j := i + 1; j < len(prices); j++ {
			if p := prices[j] - prices[i]; p > best.profit {
				best = hiddenTrade{i, j, p}
			}
		}
	}
	return best
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(121))
	for round := 0; round < 600; round++ {
		n := rng.Intn(30)
		spread := []int{3, 10, 1000}[round%3]
		prices := make([]int, n)
		for i := range prices {
			prices[i] = rng.Intn(spread) - spread/2
		}
		buy, sell, profit := BestTrade(prices)
		if got, want := (hiddenTrade{buy, sell, profit}), hiddenBrute(prices); got != want {
			t.Fatalf("round %d: BestTrade(%v) = %+v, want %+v", round, prices, got, want)
		}
	}
}

// hiddenBestProfit recomputes the best profit with a suffix maximum, independently
// of the running-minimum formulation.
func hiddenBestProfit(prices []int) int {
	best, high, seen := 0, 0, false
	for i := len(prices) - 1; i >= 0; i-- {
		if seen && high-prices[i] > best {
			best = high - prices[i]
		}
		if !seen || prices[i] > high {
			high, seen = prices[i], true
		}
	}
	return best
}

func TestHiddenLargeHistory(t *testing.T) {
	const n = 1_000_000
	run := func(name string, prices []int) hiddenTrade {
		done := make(chan hiddenTrade, 1)
		go func() {
			buy, sell, profit := BestTrade(prices)
			done <- hiddenTrade{buy, sell, profit}
		}()
		select {
		case got := <-done:
			return got
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: BestTrade took longer than 10s on %d prices", name, n)
			return hiddenTrade{}
		}
	}
	rising := make([]int, n)
	falling := make([]int, n)
	random := make([]int, n)
	x := uint32(121)
	for i := range rising {
		rising[i] = i
		falling[i] = n - i
		x = x*1664525 + 1013904223
		random[i] = int(x>>8) - (1 << 23)
	}
	if got := run("rising", rising); got != (hiddenTrade{0, n - 1, n - 1}) {
		t.Fatalf("rising: got %+v", got)
	}
	if got := run("falling", falling); got != (hiddenTrade{0, 0, 0}) {
		t.Fatalf("falling: got %+v", got)
	}
	got := run("random", random)
	if want := hiddenBestProfit(random); got.profit != want {
		t.Fatalf("random: profit = %d, want %d", got.profit, want)
	}
	if got.buy >= got.sell || random[got.sell]-random[got.buy] != got.profit {
		t.Fatalf("random: inconsistent hiddenTrade %+v", got)
	}
}
