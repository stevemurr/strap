package globs

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"*.log", "app.log", true},
		{"release-?.?.?", "release-1.2.3", true},
		{"a?c", "ac", false},
		{"a*b", "acbd", false},
		{"*", "", true},
		{"**a", "a", true},
		{"", "", true},
		{"", "a", false},
		{"a", "", false},
		{"?", "", false},
		{"?", "x", true},
		{"***", "anything", true},
		{"a?c", "abc", true},
		{"a*b", "ab", true},
		{"a*b", "acb", true},
		{"a*b", "a", false},
		{"*.log", "app.log.gz", false},
		{"*.log", ".log", true},
		{"App.log", "app.log", false},
		{"a/b", "a/b", true},
		{"a?b", "a/b", true},
		{"*aab", "aaab", true},
		{"*ab*", "xaxbx", false},
		{"*a*b", "cacb", true},
		{"?*?", "ab", true},
		{"?*?", "a", false},
		{"*?", "", false},
		{"c*a*b", "cab", true},
		{"mis*is*p*.", "mississippi.", true},
		{"mis*is*p*", "mississippi", true},
		{"b*?*?*a", "bxxa", true},
		{"b*?*?*a", "ba", false},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.name); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

// brute tries every split for every star.
func brute(pattern, name string) bool {
	if pattern == "" {
		return name == ""
	}
	if pattern[0] == '*' {
		return brute(pattern[1:], name) || (name != "" && brute(pattern, name[1:]))
	}
	if name == "" {
		return false
	}
	if pattern[0] == '?' || pattern[0] == name[0] {
		return brute(pattern[1:], name[1:])
	}
	return false
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(44))
	patternBytes := []byte("ab?*")
	nameBytes := []byte("ab")
	for round := 0; round < 3000; round++ {
		p := make([]byte, rng.Intn(7))
		for i := range p {
			p[i] = patternBytes[rng.Intn(len(patternBytes))]
		}
		n := make([]byte, rng.Intn(9))
		for i := range n {
			n[i] = nameBytes[rng.Intn(len(nameBytes))]
		}
		pattern, name := string(p), string(n)
		if got, want := Match(pattern, name), brute(pattern, name); got != want {
			t.Fatalf("round %d: Match(%q, %q) = %v, want %v", round, pattern, name, got, want)
		}
	}
}

func TestHiddenLongName(t *testing.T) {
	name := strings.Repeat("a", 30_000)
	cases := []struct {
		label   string
		pattern string
		want    bool
	}{
		{"ten stars then b", "*a*a*a*a*a*a*a*a*a*a*b", false},
		{"ten stars", "*a*a*a*a*a*a*a*a*a*a*", true},
		{"two hundred stars then b", strings.Repeat("*a", 200) + "b", false},
		{"two hundred stars", strings.Repeat("*a", 200), true},
		{"questions then b", strings.Repeat("?*", 100) + "b", false},
		{"suffix", "*aaaaaaaaaab", false},
	}
	for _, c := range cases {
		done := make(chan bool, 1)
		go func() { done <- Match(c.pattern, name) }()
		select {
		case got := <-done:
			if got != c.want {
				t.Fatalf("%s: Match(%q, 30000 x 'a') = %v, want %v", c.label, c.pattern, got, c.want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: Match took longer than 10s on a %d-byte name", c.label, len(name))
		}
	}
}
