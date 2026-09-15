package logspan

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

func hiddenSplit(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, " ")
}

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name       string
		events     string
		required   string
		start, end int
		ok         bool
	}{
		{"readme", "a d o b e c o d e b a n c", "a b c", 9, 13, true},
		{"shorter later", "x a x b a x", "a b", 3, 5, true},
		{"equal lengths", "a b a b", "a b", 0, 2, true},
		{"repeat satisfied", "a a", "a a", 0, 2, true},
		{"repeat unsatisfied", "a", "a a", 0, 0, false},
		{"empty required", "a b", "", 0, 0, false},
		{"empty events", "", "a", 0, 0, false},
		{"both empty", "", "", 0, 0, false},
		{"single hit", "a", "a", 0, 1, true},
		{"missing type", "a b c", "a d", 0, 0, false},
		{"whole log", "c b a", "a b c", 0, 3, true},
		{"filler inside", "a x x x b", "a b", 0, 5, true},
		{"repeat needs spread", "a b a c a", "a a", 0, 3, true},
		{"three of a kind", "a b a c a a", "a a a", 2, 6, true},
		{"case sensitive", "A a", "a", 1, 2, true},
		{"last position", "b b b a", "a", 3, 4, true},
		{"longer names", "login retry error retry crash", "retry error", 1, 3, true},
	}
	for _, c := range cases {
		start, end, ok := SmallestCoveringSpan(hiddenSplit(c.events), hiddenSplit(c.required))
		if start != c.start || end != c.end || ok != c.ok {
			t.Errorf("%s: SmallestCoveringSpan(%q, %q) = (%d, %d, %v), want (%d, %d, %v)", c.name, c.events, c.required, start, end, ok, c.start, c.end, c.ok)
		}
	}
}

func TestHiddenTieBreak(t *testing.T) {
	cases := []struct {
		events, required string
		start, end       int
	}{
		{"a b c a b c", "a b c", 0, 3},
		{"b a x a b", "a b", 0, 2},
		{"a x b b x a", "a b", 0, 3},
		{"c a b x b a c", "a b c", 0, 3},
		{"a a b b a a", "a b", 1, 3},
	}
	for _, c := range cases {
		start, end, ok := SmallestCoveringSpan(hiddenSplit(c.events), hiddenSplit(c.required))
		if !ok || start != c.start || end != c.end {
			t.Errorf("SmallestCoveringSpan(%q, %q) = (%d, %d, %v), want (%d, %d, true)", c.events, c.required, start, end, ok, c.start, c.end)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	events := hiddenSplit("b a c x a b")
	required := hiddenSplit("a b b")
	eventsBefore := append([]string(nil), events...)
	requiredBefore := append([]string(nil), required...)
	SmallestCoveringSpan(events, required)
	if !reflect.DeepEqual(events, eventsBefore) {
		t.Fatalf("events modified: %v", events)
	}
	if !reflect.DeepEqual(required, requiredBefore) {
		t.Fatalf("required modified: %v", required)
	}
}

func hiddenCovers(window []string, required []string) bool {
	need := map[string]int{}
	for _, r := range required {
		need[r]++
	}
	for _, e := range window {
		need[e]--
	}
	for _, n := range need {
		if n > 0 {
			return false
		}
	}
	return true
}

// hiddenBrute tries every start and grows the span until it hiddenCovers required.
func hiddenBrute(events, required []string) (int, int, bool) {
	if len(required) == 0 {
		return 0, 0, false
	}
	bestStart, bestEnd, found := 0, 0, false
	for s := range events {
		for e := s + 1; e <= len(events); e++ {
			if hiddenCovers(events[s:e], required) {
				if !found || e-s < bestEnd-bestStart {
					bestStart, bestEnd, found = s, e, true
				}
				break
			}
		}
	}
	return bestStart, bestEnd, found
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(76))
	types := []string{"a", "b", "c", "d"}
	for round := 0; round < 500; round++ {
		n := rng.Intn(25)
		events := make([]string, n)
		for i := range events {
			events[i] = types[rng.Intn(len(types))]
		}
		required := make([]string, rng.Intn(6))
		for i := range required {
			required[i] = types[rng.Intn(len(types))]
		}
		gs, ge, gok := SmallestCoveringSpan(events, required)
		ws, we, wok := hiddenBrute(events, required)
		if gs != ws || ge != we || gok != wok {
			t.Fatalf("round %d: SmallestCoveringSpan(%v, %v) = (%d, %d, %v), want (%d, %d, %v)", round, events, required, gs, ge, gok, ws, we, wok)
		}
	}
}

func TestHiddenLargeLog(t *testing.T) {
	const n = 1_000_000
	types := make([]string, 26)
	for i := range types {
		types[i] = string(rune('a' + i))
	}
	events := make([]string, n)
	x := uint32(3)
	for i := range events {
		x = x*1664525 + 1013904223
		events[i] = types[int(x>>8)%25] // a..y
	}
	// z is rare; the closest pair of z positions is 160000 and 265000.
	zAt := []int{50_000, 160_000, 265_000, 372_000, 480_000, 590_000, 701_000, 813_000, 926_000}
	for _, p := range zAt {
		events[p] = "z"
	}
	required := []string{"z", "z"}
	for i := 0; i < 48; i++ {
		required = append(required, types[i%24])
	}
	type answer struct {
		start, end int
		ok         bool
	}
	run := func(name string, required []string) answer {
		done := make(chan answer, 1)
		go func() {
			s, e, ok := SmallestCoveringSpan(events, required)
			done <- answer{s, e, ok}
		}()
		select {
		case a := <-done:
			return a
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: SmallestCoveringSpan took longer than 10s on %d events", name, n)
			return answer{}
		}
	}
	if a := run("two rare events", required); !a.ok || a.start != 160_000 || a.end != 265_001 {
		t.Fatalf("two rare events: got %+v, want {160000 265001 true}", a)
	} else if !hiddenCovers(events[a.start:a.end], required) {
		t.Fatalf("two rare events: returned span does not cover required")
	}
	if a := run("impossible", append([]string{"never"}, required...)); a.ok {
		t.Fatalf("impossible: got %+v, want ok=false", a)
	}
	if a := run("single type", []string{"z"}); !a.ok || a.start != 50_000 || a.end != 50_001 {
		t.Fatalf("single type: got %+v, want {50000 50001 true}", a)
	}
}
