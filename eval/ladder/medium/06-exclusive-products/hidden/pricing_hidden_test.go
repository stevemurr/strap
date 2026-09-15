package pricing

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
		factors []int
		want    []int
	}{
		{"readme", []int{1, 2, 3, 4}, []int{24, 12, 8, 6}},
		{"one zero", []int{2, 0, 5}, []int{0, 10, 0}},
		{"two zeros", []int{0, 3, 0}, []int{0, 0, 0}},
		{"pair", []int{-2, 3}, []int{3, -2}},
		{"signs", []int{1, -1, 1, -1}, []int{1, -1, 1, -1}},
		{"zero first", []int{0, 4, 5}, []int{20, 0, 0}},
		{"zero last", []int{4, 5, 0}, []int{0, 0, 20}},
		{"zero pair", []int{0, 9}, []int{9, 0}},
		{"both zero", []int{0, 0}, []int{0, 0}},
		{"negative and zero", []int{-3, 0, 2, -1}, []int{0, 6, 0, 0}},
		{"ones", []int{1, 1, 1}, []int{1, 1, 1}},
		{"large", []int{1 << 30, 1 << 30, 1 << 2}, []int{1 << 32, 1 << 32, 1 << 60}},
	}
	for _, c := range cases {
		got := ExclusiveProducts(c.factors)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: ExclusiveProducts(%v) = %v, want %v", c.name, c.factors, got, c.want)
		}
	}
}

func TestHiddenTooFewFactors(t *testing.T) {
	for _, factors := range [][]int{nil, {}, {7}, {0}} {
		if got := ExclusiveProducts(factors); got != nil {
			t.Errorf("ExclusiveProducts(%v) = %v, want nil", factors, got)
		}
	}
}

func TestHiddenDoesNotMutateOrAlias(t *testing.T) {
	factors := []int{3, -1, 4, 0, 5}
	before := slices.Clone(factors)
	out := ExclusiveProducts(factors)
	if !slices.Equal(factors, before) {
		t.Fatalf("input modified: %v", factors)
	}
	if len(out) > 0 && &out[0] == &factors[0] {
		t.Fatal("result aliases the input slice")
	}
}

// bruteProducts multiplies the other factors from scratch for each position.
func bruteProducts(factors []int) []int {
	if len(factors) < 2 {
		return nil
	}
	out := make([]int, len(factors))
	for i := range factors {
		p := 1
		for j, f := range factors {
			if j != i {
				p *= f
			}
		}
		out[i] = p
	}
	return out
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(238))
	for round := 0; round < 500; round++ {
		n := rng.Intn(12)
		factors := make([]int, n)
		for i := range factors {
			factors[i] = rng.Intn(7) - 3
		}
		got, want := ExclusiveProducts(factors), bruteProducts(factors)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round %d: ExclusiveProducts(%v) = %v, want %v", round, factors, got, want)
		}
	}
}

func TestHiddenLargeInput(t *testing.T) {
	const n = 1_000_000
	rng := rand.New(rand.NewSource(23838))
	base := make([]int, n)
	negatives := 0
	for i := range base {
		base[i] = 1
		if rng.Intn(2) == 0 {
			base[i] = -1
			negatives++
		}
	}
	total := 1
	if negatives%2 == 1 {
		total = -1
	}
	// Without zeros out[i] is total/factors[i], which for +-1 is total*factors[i].
	wantNoZeros := func(i int) int { return total * base[i] }

	zeroAt := rng.Intn(n)
	oneZero := slices.Clone(base)
	oneZero[zeroAt] = 0
	restProduct := total * base[zeroAt]
	wantOneZero := func(i int) int {
		if i == zeroAt {
			return restProduct
		}
		return 0
	}

	twoZeros := slices.Clone(oneZero)
	twoZeros[(zeroAt+123_457)%n] = 0
	twoZeros[(zeroAt+777_777)%n] = 0
	wantTwoZeros := func(int) int { return 0 }

	for _, shape := range []struct {
		name    string
		factors []int
		want    func(i int) int
	}{
		{"no zeros", base, wantNoZeros},
		{"one zero", oneZero, wantOneZero},
		{"several zeros", twoZeros, wantTwoZeros},
	} {
		done := make(chan []int, 1)
		go func() { done <- ExclusiveProducts(shape.factors) }()
		var got []int
		select {
		case got = <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: ExclusiveProducts took longer than 10s on %d factors", shape.name, n)
		}
		if len(got) != n {
			t.Fatalf("%s: got %d results, want %d", shape.name, len(got), n)
		}
		for i := range got {
			if want := shape.want(i); got[i] != want {
				t.Fatalf("%s: out[%d] = %d, want %d", shape.name, i, got[i], want)
			}
		}
	}
}
