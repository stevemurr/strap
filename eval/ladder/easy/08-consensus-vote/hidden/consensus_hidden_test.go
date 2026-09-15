package voting

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name  string
		votes []string
		want  string
		ok    bool
	}{
		{"readme majority", []string{"yes", "no", "yes"}, "yes", true},
		{"readme exactly half", []string{"a", "b", "c", "a"}, "", false},
		{"readme single", []string{"x"}, "x", true},
		{"readme late majority", []string{"a", "a", "b", "b", "b"}, "b", true},
		{"readme case sensitive", []string{"yes", "Yes", "yes", "no", "no"}, "", false},
		{"readme empty", nil, "", false},
		{"empty non-nil", []string{}, "", false},
		{"two equal", []string{"a", "a"}, "a", true},
		{"two different", []string{"a", "b"}, "", false},
		{"three way split", []string{"a", "b", "c"}, "", false},
		{"plurality is not majority", []string{"a", "a", "b", "c", "d"}, "", false},
		{"empty string wins", []string{"", "x", ""}, "", true},
		{"empty string loses", []string{"", "x"}, "", false},
		{"majority scattered", []string{"m", "x", "m", "y", "m", "z", "m"}, "m", true},
		{"all same", []string{"q", "q", "q", "q"}, "q", true},
		{"half of even", []string{"a", "a", "b", "b"}, "", false},
		{"majority at end", []string{"x", "y", "z", "w", "w", "w", "w"}, "w", true},
		{"trailing space differs", []string{"a", "a ", "a"}, "a", true},
	}
	for _, c := range cases {
		got, ok := Consensus(c.votes)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: Consensus(%q) = (%q, %v), want (%q, %v)", c.name, c.votes, got, ok, c.want, c.ok)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	votes := []string{"b", "a", "b", "c", "b"}
	before := append([]string(nil), votes...)
	Consensus(votes)
	if !reflect.DeepEqual(votes, before) {
		t.Fatalf("input was modified: %q", votes)
	}
}

func brute(votes []string) (string, bool) {
	for _, v := range votes {
		n := 0
		for _, w := range votes {
			if w == v {
				n++
			}
		}
		if n > len(votes)/2 {
			return v, true
		}
	}
	return "", false
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(169))
	values := []string{"a", "b", "c", ""}
	for round := 0; round < 800; round++ {
		n := rng.Intn(16)
		votes := make([]string, n)
		k := 2 + rng.Intn(3)
		for i := range votes {
			votes[i] = values[rng.Intn(k)]
		}
		if round%3 == 0 {
			// Force a leader with roughly 60% of the votes to exercise the
			// majority path as often as the no-majority path.
			leader := values[rng.Intn(k)]
			for i := range votes {
				if rng.Intn(5) < 3 {
					votes[i] = leader
				}
			}
		}
		got, ok := Consensus(votes)
		want, wantOK := brute(votes)
		if got != want || ok != wantOK {
			t.Fatalf("round %d: Consensus(%q) = (%q, %v), want (%q, %v)", round, votes, got, ok, want, wantOK)
		}
	}
}

func TestHiddenLargeElection(t *testing.T) {
	const n = 1_000_000
	votes := make([]string, n)
	for i := range votes {
		if i%2 == 1 || i == 0 {
			votes[i] = "alpha"
		} else {
			votes[i] = fmt.Sprintf("v%d", i)
		}
	}
	type answer struct {
		value string
		ok    bool
	}
	run := func(name string) answer {
		done := make(chan answer, 1)
		go func() {
			value, ok := Consensus(votes)
			done <- answer{value, ok}
		}()
		select {
		case a := <-done:
			return a
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: Consensus took longer than 10s on %d votes", name, n)
			return answer{}
		}
	}
	if a := run("scattered majority"); !a.ok || a.value != "alpha" {
		t.Fatalf("scattered majority: got %+v", a)
	}
	votes[0] = "v0"
	if a := run("exactly half"); a.ok || a.value != "" {
		t.Fatalf("exactly half: got %+v", a)
	}
	for i := range votes {
		votes[i] = fmt.Sprintf("v%d", i)
	}
	if a := run("all distinct"); a.ok || a.value != "" {
		t.Fatalf("all distinct: got %+v", a)
	}
}
