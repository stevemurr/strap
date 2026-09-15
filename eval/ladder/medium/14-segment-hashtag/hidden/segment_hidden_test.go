package hashtags

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

// check verifies that parts is a valid segmentation of text.
func check(t *testing.T, name, text string, words []string, parts []string) {
	t.Helper()
	dict := map[string]bool{}
	for _, w := range words {
		dict[w] = true
	}
	for _, p := range parts {
		if !dict[p] {
			t.Fatalf("%s: part %q is not a dictionary word", name, p)
		}
	}
	if joined := strings.Join(parts, ""); joined != text {
		t.Fatalf("%s: parts join to %d bytes, want %d bytes of text", name, len(joined), len(text))
	}
}

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		words []string
		ok    bool
	}{
		{"readme hashtag", "gonorthsummer", []string{"go", "north", "summer", "on"}, true},
		{"readme reuse", "applepenapple", []string{"apple", "pen"}, true},
		{"readme impossible", "catsandog", []string{"cats", "dog", "sand", "and", "cat"}, false},
		{"readme aaaa", "aaaa", []string{"a", "aa"}, true},
		{"readme empty text", "", []string{"a"}, true},
		{"readme no words", "a", nil, false},
		{"empty text no words", "", nil, true},
		{"single word", "hello", []string{"hello"}, true},
		{"prefix only", "hello", []string{"hell"}, false},
		{"suffix only", "hello", []string{"ello"}, false},
		{"duplicate words", "abab", []string{"ab", "ab"}, true},
		{"needs backtracking", "abcd", []string{"a", "abc", "b", "cd"}, true},
		{"greedy longest fails", "aaab", []string{"aaa", "aa", "ab"}, true},
		{"case sensitive", "Hello", []string{"hello"}, false},
		{"whole text plus parts", "leetcode", []string{"leet", "code", "leetcode"}, true},
	}
	for _, c := range cases {
		parts, ok := Segment(c.text, c.words)
		if ok != c.ok {
			t.Errorf("%s: Segment(%q, %q) ok = %v, want %v", c.name, c.text, c.words, ok, c.ok)
			continue
		}
		if !ok {
			if parts != nil {
				t.Errorf("%s: parts = %q, want nil on failure", c.name, parts)
			}
			continue
		}
		check(t, c.name, c.text, c.words, parts)
	}
}

func TestHiddenReadmeExactSplit(t *testing.T) {
	parts, ok := Segment("gonorthsummer", []string{"go", "north", "summer", "on"})
	if !ok || !reflect.DeepEqual(parts, []string{"go", "north", "summer"}) {
		t.Fatalf("got %q, %v", parts, ok)
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	words := []string{"pen", "apple", "pen"}
	before := append([]string(nil), words...)
	Segment("applepenapple", words)
	if !reflect.DeepEqual(words, before) {
		t.Fatalf("words modified: %q", words)
	}
}

func brute(text string, words []string) bool {
	if text == "" {
		return true
	}
	for _, w := range words {
		if strings.HasPrefix(text, w) && brute(text[len(w):], words) {
			return true
		}
	}
	return false
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(139))
	alphabet := "ab"
	randomWord := func(maxLen int) string {
		b := make([]byte, rng.Intn(maxLen)+1)
		for i := range b {
			b[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return string(b)
	}
	for round := 0; round < 600; round++ {
		words := make([]string, rng.Intn(5)+1)
		for i := range words {
			words[i] = randomWord(3)
		}
		var text string
		if round%2 == 0 {
			// Built from words, so a segmentation exists.
			for k := rng.Intn(5); k > 0; k-- {
				text += words[rng.Intn(len(words))]
			}
		} else {
			text = randomWord(12)
			if rng.Intn(4) == 0 {
				text = ""
			}
		}
		parts, ok := Segment(text, words)
		want := brute(text, words)
		if ok != want {
			t.Fatalf("round %d: Segment(%q, %q) ok = %v, want %v", round, text, words, ok, want)
		}
		if ok {
			check(t, "random", text, words, parts)
		} else if parts != nil {
			t.Fatalf("round %d: parts = %q on failure", round, parts)
		}
	}
}

func TestHiddenLargeInputs(t *testing.T) {
	type answer struct {
		parts []string
		ok    bool
	}
	run := func(name, text string, words []string) answer {
		done := make(chan answer, 1)
		go func() {
			parts, ok := Segment(text, words)
			done <- answer{parts, ok}
		}()
		select {
		case a := <-done:
			return a
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: Segment took longer than 10s on %d bytes", name, len(text))
			return answer{}
		}
	}

	small := []string{"a", "aa", "aaa"}
	if a := run("adversarial", strings.Repeat("a", 10_000)+"b", small); a.ok || a.parts != nil {
		t.Fatalf("adversarial: ok = %v, want false with nil parts", a.ok)
	}
	if a := run("all a", strings.Repeat("a", 10_000), small); !a.ok {
		t.Fatal("all a: want a segmentation")
	} else {
		check(t, "all a", strings.Repeat("a", 10_000), small, a.parts)
	}

	rng := rand.New(rand.NewSource(1390))
	words := make([]string, 1000)
	for i := range words {
		b := make([]byte, rng.Intn(20)+1)
		for j := range b {
			b[j] = byte('a' + rng.Intn(6))
		}
		words[i] = string(b)
	}
	var sb strings.Builder
	for {
		w := words[rng.Intn(len(words))]
		if sb.Len()+len(w) > 100_000 {
			break
		}
		sb.WriteString(w)
	}
	text := sb.String()
	if a := run("long", text, words); !a.ok {
		t.Fatal("long: want a segmentation")
	} else {
		check(t, "long", text, words, a.parts)
	}
	if a := run("long impossible", text+"Z", words); a.ok {
		t.Fatal("long impossible: want false")
	}
}
