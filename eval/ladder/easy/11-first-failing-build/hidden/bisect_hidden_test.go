package bisect

import (
	"math/bits"
	"math/rand"
	"testing"
	"time"
)

// hiddenProbe is a monotonic predicate that counts calls and records arguments
// outside 1..n.
type hiddenProbe struct {
	n, first   int
	calls      int
	outOfRange []int
}

func (p *hiddenProbe) failing(build int) bool {
	p.calls++
	if (build < 1 || build > p.n) && len(p.outOfRange) < 5 {
		p.outOfRange = append(p.outOfRange, build)
	}
	return build >= p.first
}

// hiddenBudget is 2*ceil(log2(n))+2; bits.Len(n-1) is ceil(log2(n)) for n >= 1.
func hiddenBudget(n int) int { return 2*bits.Len(uint(n-1)) + 2 }

func (p *hiddenProbe) verify(t *testing.T, got int) {
	t.Helper()
	if got != p.first {
		t.Errorf("FirstFailing(n=%d, first=%d) = %d", p.n, p.first, got)
	}
	if len(p.outOfRange) > 0 {
		t.Errorf("n=%d first=%d: failing called outside 1..n with %v", p.n, p.first, p.outOfRange)
	}
	if p.calls > hiddenBudget(p.n) {
		t.Errorf("n=%d first=%d: %d calls to failing, hiddenBudget is %d", p.n, p.first, p.calls, hiddenBudget(p.n))
	}
}

func hiddenCheck(t *testing.T, n, first int) {
	t.Helper()
	p := &hiddenProbe{n: n, first: first}
	p.verify(t, FirstFailing(n, p.failing))
}

func TestHiddenExamples(t *testing.T) {
	hiddenCheck(t, 1, 1)
	hiddenCheck(t, 5, 4)
	hiddenCheck(t, 5, 1)
	hiddenCheck(t, 8, 8)
	hiddenCheck(t, 1_000_000_000, 123_456_789)
}

func TestHiddenSmallRanges(t *testing.T) {
	for n := 1; n <= 40; n++ {
		for first := 1; first <= n; first++ {
			hiddenCheck(t, n, first)
		}
	}
}

func TestHiddenPowerOfTwoEdges(t *testing.T) {
	for k := 0; k <= 29; k++ {
		for _, n := range []int{1<<k - 1, 1 << k, 1<<k + 1} {
			if n < 1 {
				continue
			}
			for _, first := range []int{1, 2, n / 2, n/2 + 1, n - 1, n} {
				if first >= 1 && first <= n {
					hiddenCheck(t, n, first)
				}
			}
		}
	}
}

func hiddenBrute(n int, failing func(build int) bool) int {
	for b := 1; b <= n; b++ {
		if failing(b) {
			return b
		}
	}
	return n
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(278))
	for round := 0; round < 500; round++ {
		n := rng.Intn(5000) + 1
		first := rng.Intn(n) + 1
		p := &hiddenProbe{n: n, first: first}
		got := FirstFailing(n, p.failing)
		oracle := &hiddenProbe{n: n, first: first}
		if want := hiddenBrute(n, oracle.failing); got != want {
			t.Fatalf("round %d: FirstFailing(n=%d) = %d, want %d", round, n, got, want)
		}
		p.verify(t, got)
		if t.Failed() {
			t.FailNow()
		}
	}
}

func TestHiddenBillionBuilds(t *testing.T) {
	const n = 1_000_000_000
	for _, first := range []int{1, 2, 123_456_789, 536_870_912, 536_870_913, n - 1, n} {
		p := &hiddenProbe{n: n, first: first}
		done := make(chan int, 1)
		go func() { done <- FirstFailing(n, p.failing) }()
		select {
		case got := <-done:
			p.verify(t, got)
		case <-time.After(10 * time.Second):
			t.Fatalf("first=%d: FirstFailing took longer than 10s for n=%d", first, n)
		}
	}
}
