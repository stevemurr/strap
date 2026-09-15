package autocomplete

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestHiddenReadmeSequence(t *testing.T) {
	x := New()
	x.Add("apple")
	x.Add("app")
	x.Add("apple")
	checks := []struct {
		name string
		got  any
		want any
	}{
		{`Contains("app")`, x.Contains("app"), true},
		{`Contains("ap")`, x.Contains("ap"), false},
		{`HasPrefix("ap")`, x.HasPrefix("ap"), true},
		{`HasPrefix("b")`, x.HasPrefix("b"), false},
		{`CountPrefix("app")`, x.CountPrefix("app"), 2},
		{`CountPrefix("apple")`, x.CountPrefix("apple"), 1},
		{`CountPrefix("apples")`, x.CountPrefix("apples"), 0},
		{`CountPrefix("")`, x.CountPrefix(""), 2},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestHiddenEmptyIndex(t *testing.T) {
	x := New()
	if x.Contains("") || x.Contains("a") {
		t.Fatal("empty index must not contain anything")
	}
	if x.HasPrefix("") || x.HasPrefix("a") {
		t.Fatal("empty index has no prefixes")
	}
	if x.CountPrefix("") != 0 || x.CountPrefix("a") != 0 {
		t.Fatal("empty index counts must be 0")
	}
}

func TestHiddenExactVersusPrefix(t *testing.T) {
	x := New()
	x.Add("cart")
	if x.Contains("car") {
		t.Fatal(`Contains("car") must be false; only "cart" was added`)
	}
	if !x.HasPrefix("car") || !x.HasPrefix("cart") || x.HasPrefix("carts") {
		t.Fatal("HasPrefix wrong for car/cart/carts")
	}
	if x.Contains("") {
		t.Fatal(`Contains("") must be false`)
	}
	if !x.HasPrefix("") || x.CountPrefix("") != 1 {
		t.Fatal(`"" is a prefix of every term`)
	}
	x.Add("car")
	if !x.Contains("car") || x.CountPrefix("car") != 2 || x.CountPrefix("cart") != 1 || x.CountPrefix("") != 2 {
		t.Fatal("counts wrong after adding the shorter term")
	}
}

func TestHiddenDuplicatesCountOnce(t *testing.T) {
	x := New()
	for i := 0; i < 5; i++ {
		x.Add("repeat")
		x.Add("rep")
	}
	if got := x.CountPrefix(""); got != 2 {
		t.Fatalf(`CountPrefix("") = %d, want 2`, got)
	}
	if got := x.CountPrefix("rep"); got != 2 {
		t.Fatalf(`CountPrefix("rep") = %d, want 2`, got)
	}
	if got := x.CountPrefix("repe"); got != 1 {
		t.Fatalf(`CountPrefix("repe") = %d, want 1`, got)
	}
}

func TestHiddenSingleLetters(t *testing.T) {
	x := New()
	for c := byte('a'); c <= 'z'; c++ {
		x.Add(string(c))
	}
	if got := x.CountPrefix(""); got != 26 {
		t.Fatalf(`CountPrefix("") = %d, want 26`, got)
	}
	for c := byte('a'); c <= 'z'; c++ {
		if !x.Contains(string(c)) || x.CountPrefix(string(c)) != 1 || x.Contains(string(c)+"a") {
			t.Fatalf("letter %c wrong", c)
		}
	}
}

// oracle answers queries from a sorted slice of the distinct terms.
type oracle struct{ terms []string }

func newOracle(added []string) oracle {
	set := map[string]bool{}
	for _, s := range added {
		set[s] = true
	}
	terms := make([]string, 0, len(set))
	for s := range set {
		terms = append(terms, s)
	}
	sort.Strings(terms)
	return oracle{terms}
}

func (o oracle) contains(term string) bool {
	i := sort.SearchStrings(o.terms, term)
	return i < len(o.terms) && o.terms[i] == term
}

func (o oracle) countPrefix(prefix string) int {
	lo := sort.SearchStrings(o.terms, prefix)
	hi := sort.Search(len(o.terms), func(i int) bool {
		return o.terms[i] > prefix && !strings.HasPrefix(o.terms[i], prefix)
	})
	return hi - lo
}

func randomTerm(rng *rand.Rand, minLen, maxLen, letters int) string {
	b := make([]byte, rng.Intn(maxLen-minLen+1)+minLen)
	for i := range b {
		b[i] = byte('a' + rng.Intn(letters))
	}
	return string(b)
}

func TestHiddenAgainstOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(208))
	for round := 0; round < 200; round++ {
		x := New()
		var added []string
		for i := rng.Intn(20); i > 0; i-- {
			term := randomTerm(rng, 1, 5, 3)
			added = append(added, term)
			x.Add(term)
		}
		o := newOracle(added)
		for q := 0; q < 30; q++ {
			s := randomTerm(rng, 0, 6, 3)
			if q%2 == 0 && len(added) > 0 {
				term := added[rng.Intn(len(added))]
				s = term[:rng.Intn(len(term)+1)]
			}
			if got, want := x.Contains(s), o.contains(s); got != want {
				t.Fatalf("round %d: Contains(%q) = %v, want %v (terms %q)", round, s, got, want, o.terms)
			}
			if got, want := x.CountPrefix(s), o.countPrefix(s); got != want {
				t.Fatalf("round %d: CountPrefix(%q) = %d, want %d (terms %q)", round, s, got, want, o.terms)
			}
			if got, want := x.HasPrefix(s), o.countPrefix(s) > 0; got != want {
				t.Fatalf("round %d: HasPrefix(%q) = %v, want %v (terms %q)", round, s, got, want, o.terms)
			}
		}
	}
}

func TestHiddenManyTermsAndQueries(t *testing.T) {
	const terms = 100_000
	const queries = 200_000
	rng := rand.New(rand.NewSource(2080))
	added := make([]string, 0, terms+terms/5)
	seen := map[string]bool{}
	for len(seen) < terms {
		term := randomTerm(rng, 3, 12, 26)
		if seen[term] {
			continue
		}
		seen[term] = true
		added = append(added, term)
		if rng.Intn(5) == 0 {
			added = append(added, term)
		}
	}
	type query struct {
		kind int
		arg  string
	}
	qs := make([]query, queries)
	for i := range qs {
		arg := randomTerm(rng, 0, 12, 26)
		switch rng.Intn(3) {
		case 0:
			arg = added[rng.Intn(len(added))]
		case 1:
			term := added[rng.Intn(len(added))]
			arg = term[:rng.Intn(len(term)+1)]
		}
		qs[i] = query{i % 3, arg}
	}
	o := newOracle(added)

	done := make(chan error, 1)
	go func() {
		x := New()
		for _, term := range added {
			x.Add(term)
		}
		if got := x.CountPrefix(""); got != terms {
			done <- fmt.Errorf(`CountPrefix("") = %d, want %d`, got, terms)
			return
		}
		for _, q := range qs {
			switch q.kind {
			case 0:
				if got, want := x.Contains(q.arg), o.contains(q.arg); got != want {
					done <- fmt.Errorf("Contains(%q) = %v, want %v", q.arg, got, want)
					return
				}
			case 1:
				if got, want := x.HasPrefix(q.arg), o.countPrefix(q.arg) > 0; got != want {
					done <- fmt.Errorf("HasPrefix(%q) = %v, want %v", q.arg, got, want)
					return
				}
			default:
				if got, want := x.CountPrefix(q.arg), o.countPrefix(q.arg); got != want {
					done <- fmt.Errorf("CountPrefix(%q) = %d, want %d", q.arg, got, want)
					return
				}
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("%d adds and %d queries took longer than 10s", len(added), queries)
	}
}
