package recipes

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want bool
	}{
		{"readme reordered", []string{"flour", "egg", "milk"}, []string{"milk", "flour", "egg"}, true},
		{"readme multiplicity", []string{"egg", "egg", "milk"}, []string{"egg", "milk", "milk"}, false},
		{"readme case", []string{"Egg"}, []string{"egg"}, false},
		{"readme length", []string{"salt"}, []string{"salt", "salt"}, false},
		{"readme repeated", []string{"salt", "salt"}, []string{"salt", "salt"}, true},
		{"readme nil and empty", []string{}, nil, true},
		{"nil and nil", nil, nil, true},
		{"empty and empty", []string{}, []string{}, true},
		{"empty and one", nil, []string{"salt"}, false},
		{"same single", []string{"egg"}, []string{"egg"}, true},
		{"different single", []string{"egg"}, []string{"milk"}, false},
		{"trailing space", []string{"egg"}, []string{"egg "}, false},
		{"empty string ingredient", []string{"", "egg"}, []string{"egg", ""}, true},
		{"empty string count", []string{"", "", "egg"}, []string{"", "egg", "egg"}, false},
		{"disjoint", []string{"egg", "milk"}, []string{"flour", "salt"}, false},
		{"partial overlap", []string{"egg", "milk", "milk"}, []string{"egg", "egg", "milk"}, false},
		{"same set different counts", []string{"a", "a", "b"}, []string{"a", "b", "b"}, false},
		{"long shuffle", []string{"a", "b", "c", "d", "e", "a"}, []string{"e", "a", "d", "a", "c", "b"}, true},
	}
	for _, c := range cases {
		if got := SameIngredients(c.a, c.b); got != c.want {
			t.Errorf("%s: SameIngredients(%q, %q) = %v, want %v", c.name, c.a, c.b, got, c.want)
		}
		if got := SameIngredients(c.b, c.a); got != c.want {
			t.Errorf("%s (swapped): SameIngredients(%q, %q) = %v, want %v", c.name, c.b, c.a, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	a := []string{"milk", "egg", "flour", "egg"}
	b := []string{"flour", "milk", "egg", "egg"}
	aBefore := append([]string(nil), a...)
	bBefore := append([]string(nil), b...)
	if !SameIngredients(a, b) {
		t.Fatal("lists should match")
	}
	if !reflect.DeepEqual(a, aBefore) || !reflect.DeepEqual(b, bBefore) {
		t.Fatalf("inputs were modified: %q %q", a, b)
	}
}

func count(list []string, name string) int {
	n := 0
	for _, v := range list {
		if v == name {
			n++
		}
	}
	return n
}

func brute(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, name := range a {
		if count(a, name) != count(b, name) {
			return false
		}
	}
	return true
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(242))
	names := []string{"egg", "milk", "flour", "salt"}
	pick := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = names[rng.Intn(len(names))]
		}
		return out
	}
	for round := 0; round < 600; round++ {
		a := pick(rng.Intn(9))
		var b []string
		if round%2 == 0 {
			b = make([]string, len(a))
			for i, p := range rng.Perm(len(a)) {
				b[i] = a[p]
			}
			if len(b) > 0 && rng.Intn(3) == 0 {
				b[rng.Intn(len(b))] = names[rng.Intn(len(names))]
			}
		} else {
			b = pick(rng.Intn(9))
		}
		if got, want := SameIngredients(a, b), brute(a, b); got != want {
			t.Fatalf("round %d: SameIngredients(%q, %q) = %v, want %v", round, a, b, got, want)
		}
	}
}

func TestHiddenLargeLists(t *testing.T) {
	const n = 1_000_000
	a := make([]string, n)
	for i := range a {
		a[i] = fmt.Sprintf("ingredient-%d", i)
	}
	b := make([]string, n)
	for i := range b {
		b[i] = a[n-1-i]
	}
	run := func(name string, a, b []string) bool {
		done := make(chan bool, 1)
		go func() { done <- SameIngredients(a, b) }()
		select {
		case got := <-done:
			return got
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: SameIngredients took longer than 10s on %d names each", name, n)
			return false
		}
	}
	if !run("reversed", a, b) {
		t.Fatal("reversed copy should match")
	}
	b[0] = a[1]
	if run("one name doubled", a, b) {
		t.Fatal("list with one name doubled and one missing should not match")
	}
}
