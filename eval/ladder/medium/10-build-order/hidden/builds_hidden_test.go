package builds

import (
	"fmt"
	"math/rand"
	"slices"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name    string
		targets []string
		deps    [][2]string
		want    []string
	}{
		{"readme", []string{"app", "lib", "test"}, [][2]string{{"lib", "app"}, {"app", "test"}}, []string{"lib", "app", "test"}},
		{"no deps sorts", []string{"c", "b", "a"}, nil, []string{"a", "b", "c"}},
		{"smallest ready first", []string{"a", "b", "c"}, [][2]string{{"c", "a"}}, []string{"b", "c", "a"}},
		{"single", []string{"only"}, nil, []string{"only"}},
		{"chain reversed", []string{"a", "b", "c", "d"}, [][2]string{{"d", "c"}, {"c", "b"}, {"b", "a"}}, []string{"d", "c", "b", "a"}},
		{"duplicate deps", []string{"x", "y"}, [][2]string{{"y", "x"}, {"y", "x"}, {"y", "x"}}, []string{"y", "x"}},
		{"diamond", []string{"d", "c", "b", "a"}, [][2]string{{"a", "b"}, {"a", "c"}, {"b", "d"}, {"c", "d"}}, []string{"a", "b", "c", "d"}},
		{"late small name", []string{"z", "a", "m"}, [][2]string{{"z", "a"}, {"m", "z"}}, []string{"m", "z", "a"}},
		{"independent components", []string{"b2", "a2", "b1", "a1"}, [][2]string{{"b2", "b1"}, {"a2", "a1"}}, []string{"a2", "a1", "b2", "b1"}},
		{"prefix names", []string{"lib", "li", "l"}, [][2]string{{"lib", "l"}}, []string{"li", "lib", "l"}},
	}
	for _, c := range cases {
		got, err := Order(c.targets, c.deps)
		if err != nil {
			t.Errorf("%s: Order(%q, %v) returned error %v", c.name, c.targets, c.deps, err)
			continue
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: Order(%q, %v) = %q, want %q", c.name, c.targets, c.deps, got, c.want)
		}
	}
}

func TestHiddenEmpty(t *testing.T) {
	got, err := Order(nil, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("Order(nil, nil) = %q, %v; want empty, nil", got, err)
	}
	got, err = Order([]string{}, [][2]string{})
	if err != nil || len(got) != 0 {
		t.Fatalf("Order([], []) = %q, %v; want empty, nil", got, err)
	}
}

func TestHiddenErrors(t *testing.T) {
	cases := []struct {
		name    string
		targets []string
		deps    [][2]string
	}{
		{"two-cycle", []string{"a", "b"}, [][2]string{{"a", "b"}, {"b", "a"}}},
		{"self dependency", []string{"a", "b"}, [][2]string{{"a", "a"}}},
		{"long cycle", []string{"a", "b", "c", "d"}, [][2]string{{"a", "b"}, {"b", "c"}, {"c", "d"}, {"d", "b"}}},
		{"cycle beside free targets", []string{"a", "b", "c", "d"}, [][2]string{{"c", "d"}, {"d", "c"}}},
		{"unknown after", []string{"a"}, [][2]string{{"a", "zzz"}}},
		{"unknown before", []string{"a"}, [][2]string{{"zzz", "a"}}},
		{"unknown with no targets", nil, [][2]string{{"a", "b"}}},
		{"duplicate target", []string{"a", "a"}, nil},
		{"duplicate target with deps", []string{"a", "b", "a"}, [][2]string{{"a", "b"}}},
	}
	for _, c := range cases {
		got, err := Order(c.targets, c.deps)
		if err == nil {
			t.Errorf("%s: Order(%q, %v) = %q, want error", c.name, c.targets, c.deps, got)
			continue
		}
		if got != nil {
			t.Errorf("%s: Order returned %q with error; want nil order", c.name, got)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	targets := []string{"c", "b", "a"}
	deps := [][2]string{{"c", "a"}, {"b", "c"}}
	targetsBefore := slices.Clone(targets)
	depsBefore := slices.Clone(deps)
	if _, err := Order(targets, deps); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(targets, targetsBefore) {
		t.Fatalf("targets modified: %q", targets)
	}
	if !slices.Equal(deps, depsBefore) {
		t.Fatalf("deps modified: %v", deps)
	}
}

// bruteOrder enumerates every permutation of the targets and keeps the
// smallest one that satisfies all deps; ok is false when none does.
func bruteOrder(targets []string, deps [][2]string) (best []string, ok bool) {
	perm := slices.Clone(targets)
	var walk func(k int)
	walk = func(k int) {
		if k == len(perm) {
			pos := make(map[string]int, len(perm))
			for i, name := range perm {
				pos[name] = i
			}
			for _, d := range deps {
				if pos[d[0]] >= pos[d[1]] {
					return
				}
			}
			if !ok || slices.Compare(perm, best) < 0 {
				best = slices.Clone(perm)
				ok = true
			}
			return
		}
		for i := k; i < len(perm); i++ {
			perm[k], perm[i] = perm[i], perm[k]
			walk(k + 1)
			perm[k], perm[i] = perm[i], perm[k]
		}
	}
	walk(0)
	return best, ok
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(210))
	pool := []string{"api", "app", "cli", "db", "lib", "net", "ui"}
	for round := 0; round < 400; round++ {
		names := slices.Clone(pool)
		rng.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
		targets := names[:rng.Intn(7)]
		deps := make([][2]string, rng.Intn(9))
		for i := range deps {
			if len(targets) == 0 {
				deps = nil
				break
			}
			deps[i] = [2]string{targets[rng.Intn(len(targets))], targets[rng.Intn(len(targets))]}
		}
		want, valid := bruteOrder(targets, deps)
		got, err := Order(targets, deps)
		switch {
		case !valid && err == nil:
			t.Fatalf("round %d: Order(%q, %v) = %q, want cycle error", round, targets, deps, got)
		case valid && err != nil:
			t.Fatalf("round %d: Order(%q, %v) returned error %v, want %q", round, targets, deps, err, want)
		case valid && !slices.Equal(got, want) && !(len(got) == 0 && len(want) == 0):
			t.Fatalf("round %d: Order(%q, %v) = %q, want %q", round, targets, deps, got, want)
		}
	}
}

func TestHiddenLargeGraph(t *testing.T) {
	const n = 200_000
	const m = 500_000
	rng := rand.New(rand.NewSource(21010))
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("t%06d", i)
	}
	shuffledTargets := slices.Clone(names)
	rng.Shuffle(len(shuffledTargets), func(i, j int) {
		shuffledTargets[i], shuffledTargets[j] = shuffledTargets[j], shuffledTargets[i]
	})

	// Forward: every dependency points from a smaller name to a larger one,
	// so the sorted names are the answer.
	forward := make([][2]string, 0, m)
	for len(forward) < m {
		a, b := rng.Intn(n), rng.Intn(n)
		if a == b {
			continue
		}
		if a > b {
			a, b = b, a
		}
		forward = append(forward, [2]string{names[a], names[b]})
	}

	// Backward: a chain from the largest name down to the smallest plus random
	// dependencies that agree with it, so the answer is the reverse of the
	// sorted names and only one target is ever ready.
	backward := make([][2]string, 0, m)
	for i := n - 1; i > 0; i-- {
		backward = append(backward, [2]string{names[i], names[i-1]})
	}
	for len(backward) < m {
		a, b := rng.Intn(n), rng.Intn(n)
		if a == b {
			continue
		}
		if a < b {
			a, b = b, a
		}
		backward = append(backward, [2]string{names[a], names[b]})
	}
	reversed := slices.Clone(names)
	slices.Reverse(reversed)

	// Cyclic: the backward chain plus one edge closing it.
	cyclic := append(slices.Clone(backward), [2]string{names[0], names[n-1]})

	for _, shape := range []struct {
		name string
		deps [][2]string
		want []string
	}{
		{"forward", forward, names},
		{"backward chain", backward, reversed},
		{"cycle", cyclic, nil},
	} {
		type answer struct {
			order []string
			err   error
		}
		done := make(chan answer, 1)
		go func() {
			order, err := Order(shuffledTargets, shape.deps)
			done <- answer{order, err}
		}()
		var got answer
		select {
		case got = <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: Order took longer than 10s on %d targets and %d deps", shape.name, n, len(shape.deps))
		}
		if shape.want == nil {
			if got.err == nil {
				t.Fatalf("%s: want a cycle error, got an order of %d targets", shape.name, len(got.order))
			}
			continue
		}
		if got.err != nil {
			t.Fatalf("%s: unexpected error %v", shape.name, got.err)
		}
		if len(got.order) != n {
			t.Fatalf("%s: got %d targets, want %d", shape.name, len(got.order), n)
		}
		for i := range shape.want {
			if got.order[i] != shape.want[i] {
				t.Fatalf("%s: order[%d] = %q, want %q", shape.name, i, got.order[i], shape.want[i])
			}
		}
	}
}
