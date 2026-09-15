package collation

import (
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name   string
		sorted []string
		want   string
		err    bool
	}{
		{"readme chain", []string{"wrt", "wrf", "er", "ett", "rftt"}, "wertf", false},
		{"readme partial", []string{"dc", "db", "a"}, "cbda", false},
		{"single word", []string{"bca"}, "abc", false},
		{"prefix first", []string{"ab", "abc"}, "abc", false},
		{"cycle", []string{"z", "x", "z"}, "", true},
		{"prefix violation", []string{"abc", "ab"}, "", true},
		{"empty list", nil, "", false},
		{"reversed pair", []string{"z", "x"}, "zx", false},
		{"repeated word", []string{"z", "z"}, "z", false},
		{"empty word first", []string{"", "a"}, "a", false},
		{"empty word last", []string{"a", ""}, "", true},
		{"only empty words", []string{"", ""}, "", false},
		{"indirect cycle", []string{"ba", "bb", "ab"}, "", true},
		{"long cycle", []string{"a", "b", "c", "a"}, "", true},
		{"fully determined", []string{"c", "b", "a"}, "cba", false},
		{"unconstrained letters", []string{"b", "a"}, "ba", false},
		{"deep difference", []string{"aaaz", "aaay"}, "azy", false},
		{"same prefix chain", []string{"ab", "ac", "ad"}, "abcd", false},
		{"prefix violation later", []string{"a", "bcd", "bc"}, "", true},
		{"cycle and prefix", []string{"ba", "ab", "b", "bc", "b"}, "", true},
	}
	for _, c := range cases {
		got, err := InferOrder(c.sorted)
		if c.err {
			if err == nil {
				t.Errorf("%s: InferOrder(%q) = %q, nil; want an error", c.name, c.sorted, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: InferOrder(%q) = %q, %v; want %q, nil", c.name, c.sorted, got, err, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	sorted := []string{"wrt", "wrf", "er", "ett", "rftt"}
	before := append([]string(nil), sorted...)
	InferOrder(sorted)
	if !reflect.DeepEqual(sorted, before) {
		t.Fatalf("input modified: %v", sorted)
	}
}

// hiddenLessUnder compares two words under a letter order given as rank per letter.
func hiddenLessUnder(a, b string, rank map[byte]int) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return rank[a[i]] < rank[b[i]]
		}
	}
	return len(a) < len(b)
}

func hiddenSortedUnder(words []string, order string) bool {
	rank := map[byte]int{}
	for i := 0; i < len(order); i++ {
		rank[order[i]] = i
	}
	for i := 1; i < len(words); i++ {
		if hiddenLessUnder(words[i], words[i-1], rank) {
			return false
		}
	}
	return true
}

// hiddenBrute tries every permutation of the letters that appear and keeps the
// lexicographically smallest one under which the list is sorted.
func hiddenBrute(words []string) (string, bool) {
	seen := map[byte]bool{}
	var letters []byte
	for _, w := range words {
		for i := 0; i < len(w); i++ {
			if !seen[w[i]] {
				seen[w[i]] = true
				letters = append(letters, w[i])
			}
		}
	}
	sort.Slice(letters, func(i, j int) bool { return letters[i] < letters[j] })
	best, found := "", false
	var permute func(prefix []byte, rest []byte)
	permute = func(prefix []byte, rest []byte) {
		if len(rest) == 0 {
			if order := string(prefix); hiddenSortedUnder(words, order) && (!found || order < best) {
				best, found = order, true
			}
			return
		}
		for i := range rest {
			next := append(append([]byte(nil), rest[:i]...), rest[i+1:]...)
			permute(append(prefix, rest[i]), next)
		}
	}
	permute(nil, letters)
	return best, found
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(269))
	alphabet := "abcde"
	randomWord := func() string {
		b := make([]byte, rng.Intn(4))
		for i := range b {
			b[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return string(b)
	}
	for round := 0; round < 400; round++ {
		words := make([]string, rng.Intn(7))
		for i := range words {
			words[i] = randomWord()
		}
		if round%2 == 1 {
			// Sort under a random letter order so the list is usually valid.
			perm := []byte(alphabet)
			rng.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
			rank := map[byte]int{}
			for i, l := range perm {
				rank[l] = i
			}
			sort.SliceStable(words, func(i, j int) bool { return hiddenLessUnder(words[i], words[j], rank) })
		}
		got, err := InferOrder(words)
		want, ok := hiddenBrute(words)
		if !ok {
			if err == nil {
				t.Fatalf("round %d: InferOrder(%q) = %q, nil; want an error", round, words, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Fatalf("round %d: InferOrder(%q) = %q, %v; want %q, nil", round, words, got, err, want)
		}
	}
}

func TestHiddenLargeList(t *testing.T) {
	const n = 100_000
	// Catalogue codes share a long prefix, so comparing two words costs more
	// than a glance at their first letters.
	const prefix = "partnercatal"
	rng := rand.New(rand.NewSource(7))
	perm := []byte("abcdefghijklmnopqrstuvwxyz")
	rng.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
	rank := map[byte]int{}
	for i, l := range perm {
		rank[l] = i
	}
	// One single-letter word per letter pins the whole chain, so exactly one
	// order fits and it is perm.
	words := make([]string, 0, n)
	for i := 0; i < 26; i++ {
		words = append(words, string(perm[i]))
	}
	for len(words) < n {
		b := make([]byte, rng.Intn(8)+1)
		for i := range b {
			b[i] = 'a' + byte(rng.Intn(26))
		}
		words = append(words, prefix+string(b))
	}
	sort.Slice(words, func(i, j int) bool { return hiddenLessUnder(words[i], words[j], rank) })
	if !hiddenSortedUnder(words, string(perm)) {
		t.Fatal("test setup: list is not sorted under the chosen order")
	}
	type answer struct {
		order string
		err   error
	}
	done := make(chan answer, 1)
	go func() {
		order, err := InferOrder(words)
		done <- answer{order, err}
	}()
	select {
	case a := <-done:
		if a.err != nil || a.order != string(perm) {
			t.Fatalf("InferOrder on %d words = %q, %v; want %q, nil", n, a.order, a.err, string(perm))
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("InferOrder took longer than 10s on %d words", n)
	}
}
