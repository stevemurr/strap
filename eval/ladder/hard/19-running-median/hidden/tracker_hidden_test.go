package latency

import (
	"math/rand"
	"sort"
	"strconv"
	"testing"
	"time"
)

func TestHiddenReadmeSequence(t *testing.T) {
	tr := New()
	if got := tr.Median(); got != 0 {
		t.Fatalf("empty Median() = %v, want 0", got)
	}
	steps := []struct {
		add  int
		want float64
	}{
		{5, 5}, {2, 3.5}, {9, 5}, {-4, 3.5}, {2, 2},
	}
	for i, s := range steps {
		tr.Add(s.add)
		if got := tr.Median(); got != s.want {
			t.Fatalf("step %d: after Add(%d) Median() = %v, want %v", i, s.add, got, s.want)
		}
	}
}

func TestHiddenEdgeCases(t *testing.T) {
	tr := New()
	tr.Add(-7)
	if got := tr.Median(); got != -7 {
		t.Fatalf("single negative: %v", got)
	}
	tr.Add(-7)
	if got := tr.Median(); got != -7 {
		t.Fatalf("two equal: %v", got)
	}
	tr.Add(1_000_000)
	if got := tr.Median(); got != -7 {
		t.Fatalf("outlier must not move the median: %v", got)
	}
	tr.Add(-1_000_000)
	if got := tr.Median(); got != -7 {
		t.Fatalf("symmetric outliers: %v", got)
	}
	tr.Add(0)
	if got := tr.Median(); got != -7 {
		t.Fatalf("five samples: %v", got)
	}
	tr.Add(0)
	if got := tr.Median(); got != -3.5 {
		t.Fatalf("six samples: %v", got)
	}
}

func TestHiddenIndependentTrackers(t *testing.T) {
	a, b := New(), New()
	a.Add(10)
	b.Add(20)
	if a.Median() != 10 || b.Median() != 20 {
		t.Fatalf("trackers share state: %v %v", a.Median(), b.Median())
	}
}

func hiddenSortedMedian(samples []int) float64 {
	s := append([]int(nil), samples...)
	sort.Ints(s)
	n := len(s)
	if n%2 == 1 {
		return float64(s[n/2])
	}
	return float64(s[n/2-1]+s[n/2]) / 2
}

func TestHiddenAgainstSortedCopy(t *testing.T) {
	rng := rand.New(rand.NewSource(295))
	for round := 0; round < 200; round++ {
		tr := New()
		spread := []int{3, 10, 1000, 2_000_001}[round%4]
		var samples []int
		for i := 0; i < rng.Intn(80)+1; i++ {
			v := rng.Intn(spread) - spread/2
			samples = append(samples, v)
			tr.Add(v)
			if got, want := tr.Median(), hiddenSortedMedian(samples); got != want {
				t.Fatalf("round %d: after %d samples Median() = %v, want %v", round, len(samples), got, want)
			}
		}
	}
}

func TestHiddenLargeStream(t *testing.T) {
	const n = 1_000_000
	rng := rand.New(rand.NewSource(9))
	random := make([]int, n)
	increasing := make([]int, n)
	for i := range random {
		random[i] = rng.Intn(2_000_001) - 1_000_000
		increasing[i] = i
	}
	checkpoints := map[int]bool{1: true, 2: true, 1000: true, 12345: true, 250_000: true, 500_001: true, n - 1: true, n: true}
	for _, shape := range []struct {
		name string
		data []int
	}{
		{"random", random},
		{"increasing", increasing},
	} {
		done := make(chan string, 1)
		go func() {
			tr := New()
			for i, v := range shape.data {
				tr.Add(v)
				got := tr.Median()
				count := i + 1
				if shape.name == "increasing" {
					if want := float64(count-1) / 2; got != want {
						done <- "after " + strconv.Itoa(count) + " samples Median() is wrong"
						return
					}
				} else if checkpoints[count] {
					if want := hiddenSortedMedian(shape.data[:count]); got != want {
						done <- "after " + strconv.Itoa(count) + " samples Median() is wrong"
						return
					}
				}
			}
			done <- ""
		}()
		select {
		case msg := <-done:
			if msg != "" {
				t.Fatalf("%s: %s", shape.name, msg)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: %d adds with a Median after each took longer than 10s", shape.name, n)
		}
	}
}
