package slots

import (
	"math/rand"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"readme mixed", []string{"ann", "", "bo", "", "cy"}, []string{"ann", "bo", "cy", "", ""}},
		{"readme leading gaps", []string{"", "", "di"}, []string{"di", "", ""}},
		{"readme whitespace is a value", []string{"", " ", ""}, []string{" ", "", ""}},
		{"readme all empty", []string{"", ""}, []string{"", ""}},
		{"readme no gaps", []string{"ann", "bo"}, []string{"ann", "bo"}},
		{"readme empty", []string{}, []string{}},
		{"nil", nil, nil},
		{"single value", []string{"x"}, []string{"x"}},
		{"single gap", []string{""}, []string{""}},
		{"trailing gaps already", []string{"a", "b", "", ""}, []string{"a", "b", "", ""}},
		{"gap first", []string{"", "a"}, []string{"a", ""}},
		{"gap last", []string{"a", ""}, []string{"a", ""}},
		{"duplicates keep order", []string{"", "b", "a", "", "b", "a"}, []string{"b", "a", "b", "a", "", ""}},
		{"tab is a value", []string{"", "\t", "z"}, []string{"\t", "z", ""}},
	}
	for _, c := range cases {
		n := len(c.in)
		Compact(c.in)
		if len(c.in) != n {
			t.Errorf("%s: length changed to %d", c.name, len(c.in))
			continue
		}
		if len(c.in) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(c.in, c.want) {
			t.Errorf("%s: after Compact = %q, want %q", c.name, c.in, c.want)
		}
	}
}

func TestHiddenCallerSeesResult(t *testing.T) {
	roster := []string{"", "ann", "", "bo"}
	Compact(roster)
	if want := []string{"ann", "bo", "", ""}; !reflect.DeepEqual(roster, want) {
		t.Fatalf("caller's slice = %q, want %q", roster, want)
	}
}

func TestHiddenOnlyWindowIsTouched(t *testing.T) {
	full := []string{"keep", "", "x", "", "y", "tail"}
	Compact(full[1:5])
	if want := []string{"keep", "x", "y", "", "", "tail"}; !reflect.DeepEqual(full, want) {
		t.Fatalf("backing array = %q, want %q", full, want)
	}
}

func brute(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	for len(out) < len(in) {
		out = append(out, "")
	}
	return out
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(283))
	values := []string{"", "", "a", "b", " "}
	for round := 0; round < 600; round++ {
		in := make([]string, rng.Intn(20))
		for i := range in {
			in[i] = values[rng.Intn(len(values))]
		}
		want := brute(in)
		Compact(in)
		if len(in) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(in, want) {
			t.Fatalf("round %d: after Compact = %q, want %q", round, in, want)
		}
	}
}

func TestHiddenLargeRoster(t *testing.T) {
	const n = 1_000_000
	roster := make([]string, n)
	for i := range roster {
		if i%2 == 1 {
			roster[i] = strconv.Itoa(i)
		}
	}
	done := make(chan struct{})
	go func() {
		Compact(roster)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("Compact took longer than 10s on %d slots", n)
	}
	if len(roster) != n {
		t.Fatalf("length changed to %d", len(roster))
	}
	for i := 0; i < n/2; i++ {
		if want := strconv.Itoa(2*i + 1); roster[i] != want {
			t.Fatalf("slot %d = %q, want %q", i, roster[i], want)
		}
	}
	for i := n / 2; i < n; i++ {
		if roster[i] != "" {
			t.Fatalf("slot %d = %q, want empty", i, roster[i])
		}
	}
}
