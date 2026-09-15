package budgetpair

import (
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name   string
		prices []int
		budget int
		i, j   int
		ok     bool
	}{
		{"readme", []int{25, 40, 15, 60}, 55, 1, 2, true},
		{"same price twice", []int{3, 3}, 6, 0, 1, true},
		{"classic", []int{2, 7, 11, 15}, 9, 0, 1, true},
		{"no pair", []int{1, 2, 3}, 7, 0, 0, false},
		{"empty", nil, 0, 0, 0, false},
		{"single", []int{5}, 5, 0, 0, false},
		{"zeros", []int{0, 0}, 0, 0, 1, true},
		{"negative credit", []int{-10, 30, 20, 10}, 20, 0, 1, true},
		{"item cannot pair with itself", []int{5, 1, 4}, 10, 0, 0, false},
		{"later pair only", []int{5, 1, 4, 1, 5}, 10, 0, 4, true},
		{"negative budget", []int{-3, -4, 2}, -7, 0, 1, true},
	}
	for _, c := range cases {
		i, j, ok := PairForBudget(c.prices, c.budget)
		if i != c.i || j != c.j || ok != c.ok {
			t.Errorf("%s: PairForBudget(%v, %d) = (%d, %d, %v), want (%d, %d, %v)", c.name, c.prices, c.budget, i, j, ok, c.i, c.j, c.ok)
		}
	}
}

func TestHiddenTieBreak(t *testing.T) {
	cases := []struct {
		prices []int
		budget int
		i, j   int
	}{
		{[]int{4, 6, 4, 6, 4}, 10, 0, 1},
		{[]int{1, 9, 9, 1}, 10, 0, 1},
		{[]int{5, 5, 5}, 10, 0, 1},
		{[]int{8, 2, 3, 7, 2}, 10, 0, 1},
		{[]int{1, 2, 4, 3, 3}, 6, 1, 2},
		{[]int{7, 1, 5, 3, 3, 5}, 8, 0, 1},
	}
	for _, c := range cases {
		i, j, ok := PairForBudget(c.prices, c.budget)
		if !ok || i != c.i || j != c.j {
			t.Errorf("PairForBudget(%v, %d) = (%d, %d, %v), want (%d, %d, true)", c.prices, c.budget, i, j, ok, c.i, c.j)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	prices := []int{9, 4, 7, 1}
	PairForBudget(prices, 8)
	if prices[0] != 9 || prices[1] != 4 || prices[2] != 7 || prices[3] != 1 {
		t.Fatalf("input was modified: %v", prices)
	}
}

func TestHiddenLargeCatalog(t *testing.T) {
	const n = 1_000_000
	prices := make([]int, n)
	for i := range prices {
		prices[i] = 2*i + 1
	}
	type answer struct {
		i, j int
		ok   bool
	}
	run := func(budget int) answer {
		done := make(chan answer, 1)
		go func() {
			i, j, ok := PairForBudget(prices, budget)
			done <- answer{i, j, ok}
		}()
		select {
		case a := <-done:
			return a
		case <-time.After(10 * time.Second):
			t.Fatalf("PairForBudget took longer than 10s on %d prices", n)
			return answer{}
		}
	}
	if a := run(4*n - 4); !a.ok || a.i != n-2 || a.j != n-1 {
		t.Fatalf("unique last pair: got %+v", a)
	}
	if a := run(3); a.ok {
		t.Fatalf("two odd prices cannot sum to 3: got %+v", a)
	}
}
