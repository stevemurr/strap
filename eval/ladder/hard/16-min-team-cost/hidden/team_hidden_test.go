package hiring

import (
	"math"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

const tolerance = 1e-6

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name    string
		quality []int
		wage    []int
		k       int
		want    float64
	}{
		{"readme", []int{10, 20, 5}, []int{70, 50, 30}, 2, 105},
		{"fractional", []int{3, 1, 10, 10, 1}, []int{4, 8, 2, 2, 7}, 3, 92.0 / 3},
		{"hire everyone", []int{4, 2}, []int{8, 3}, 2, 12},
		{"single", []int{5}, []int{7}, 1, 7},
		{"k too large", []int{1, 2, 3}, []int{1, 1, 1}, 4, 0},
		{"k zero", []int{1, 2, 3}, []int{1, 1, 1}, 0, 0},
		{"k negative", []int{1}, []int{1}, -1, 0},
		{"empty", nil, nil, 1, 0},
		{"pick cheapest single", []int{1, 1, 1}, []int{9, 3, 5}, 1, 3},
		{"low ratio but huge quality", []int{100, 1, 1}, []int{100, 2, 2}, 2, 4},
		{"equal ratios", []int{2, 4, 6}, []int{4, 8, 12}, 2, 12},
		{"rate set by second cheapest ratio", []int{1, 10, 2}, []int{1, 10, 6}, 2, 9},
	}
	for _, c := range cases {
		got := MinTeamCost(c.quality, c.wage, c.k)
		if math.Abs(got-c.want) > tolerance {
			t.Errorf("%s: MinTeamCost(%v, %v, %d) = %v, want %v", c.name, c.quality, c.wage, c.k, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	quality := []int{7, 3, 5, 1}
	wage := []int{20, 9, 5, 4}
	q, w := append([]int(nil), quality...), append([]int(nil), wage...)
	MinTeamCost(quality, wage, 2)
	if !reflect.DeepEqual(quality, q) || !reflect.DeepEqual(wage, w) {
		t.Fatalf("inputs modified: %v %v", quality, wage)
	}
}

// brute tries every team of k workers; the team's rate is its largest ratio.
func brute(quality, wage []int, k int) float64 {
	n := len(quality)
	if k < 1 || k > n {
		return 0
	}
	best := math.Inf(1)
	for mask := 0; mask < 1<<n; mask++ {
		size, sum, rate := 0, 0, 0.0
		for i := 0; i < n; i++ {
			if mask&(1<<i) == 0 {
				continue
			}
			size++
			sum += quality[i]
			if r := float64(wage[i]) / float64(quality[i]); r > rate {
				rate = r
			}
		}
		if size == k && rate*float64(sum) < best {
			best = rate * float64(sum)
		}
	}
	return best
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(857))
	for round := 0; round < 400; round++ {
		n := rng.Intn(9) + 1
		quality := make([]int, n)
		wage := make([]int, n)
		for i := range quality {
			quality[i] = rng.Intn(20) + 1
			wage[i] = rng.Intn(50) + 1
		}
		k := rng.Intn(n+2) - 1
		got, want := MinTeamCost(quality, wage, k), brute(quality, wage, k)
		if math.Abs(got-want) > tolerance {
			t.Fatalf("round %d: MinTeamCost(%v, %v, %d) = %v, want %v", round, quality, wage, k, got, want)
		}
	}
}

func TestHiddenLargePool(t *testing.T) {
	const n = 100_000
	const k = 1_000
	// Qualities are a shuffled 1..n; the wage/quality ratio grows with the
	// quality in steps of 1,000, so the cheapest team is qualities 1..1000 at
	// rate 1: cost 500,500.
	rng := rand.New(rand.NewSource(3))
	quality := rng.Perm(n)
	wage := make([]int, n)
	for i := range quality {
		quality[i]++
		wage[i] = quality[i] * (1 + (quality[i]-1)/1000)
	}
	done := make(chan float64, 1)
	go func() { done <- MinTeamCost(quality, wage, k) }()
	select {
	case got := <-done:
		if math.Abs(got-500_500) > tolerance {
			t.Fatalf("got %v, want 500500", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("MinTeamCost took longer than 10s on %d workers with k=%d", n, k)
	}
}
