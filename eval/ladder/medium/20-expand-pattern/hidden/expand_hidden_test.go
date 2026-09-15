package fixtures

import (
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		pattern string
		want    string
	}{
		{"3[a]2[bc]", "aaabcbc"},
		{"3[a2[c]]", "accaccacc"},
		{"2[abc]3[cd]ef", "abcabccdcdcdef"},
		{"abc", "abc"},
		{"", ""},
		{"10[a]", "aaaaaaaaaa"},
		{"1[x]", "x"},
		{"2[a3[b]]", "abbbabbb"},
		{"12[ab]", strings.Repeat("ab", 12)},
		{"a2[b]c", "abbc"},
		{"2[2[2[z]]]", "zzzzzzzz"},
		{"1[1[1[1[q]]]]", "q"},
		{"3[ab]2[c]d", "abababccd"},
	}
	for _, c := range cases {
		got, err := Expand(c.pattern)
		if err != nil {
			t.Errorf("Expand(%q) returned error %v", c.pattern, err)
			continue
		}
		if got != c.want {
			t.Errorf("Expand(%q) = %q, want %q", c.pattern, got, c.want)
		}
	}
}

func TestHiddenInvalid(t *testing.T) {
	patterns := []string{
		"2[a",
		"ab]",
		"][",
		"3[ab]]",
		"[ab]",
		"a[b]",
		"3a",
		"3",
		"a3",
		"0[a]",
		"03[a]",
		"3[]",
		"3[2[]]",
		"3[A]",
		"a b",
		"a,b",
		"3[a]\n",
		"3[a]é",
		"-1[a]",
		"3[a]]",
		"2[3[a]",
		"99999999999999999999[a]",
	}
	for _, p := range patterns {
		got, err := Expand(p)
		if err == nil {
			t.Errorf("Expand(%q) = %q, want an error", p, got)
		} else if got != "" {
			t.Errorf("Expand(%q) returned %q alongside an error, want \"\"", p, got)
		}
	}
}

func TestHiddenLimit(t *testing.T) {
	const limit = 10_000_000
	ok := []struct {
		pattern string
		length  int
	}{
		{"10000000[a]", limit},
		{"2[5000000[a]]", limit},
		{"5000000[a]5000000[b]", limit},
		{"9999999[a]b", limit},
	}
	for _, c := range ok {
		got, err := Expand(c.pattern)
		if err != nil {
			t.Errorf("Expand(%q) returned error %v", c.pattern, err)
			continue
		}
		if len(got) != c.length {
			t.Errorf("Expand(%q) has %d bytes, want %d", c.pattern, len(got), c.length)
		}
	}
	tooLong := []string{
		"10000001[a]",
		"2[5000001[a]]",
		"5000000[a]5000001[b]",
		"10000000[a]b",
		"10000000[ab]",
		"1000[1000[1000[1000[a]]]]",
		"100000000000[a]",
	}
	for _, p := range tooLong {
		got, err := Expand(p)
		if err == nil {
			t.Errorf("Expand(%q) produced %d bytes, want an error", p, len(got))
		} else if got != "" {
			t.Errorf("Expand(%q) returned %d bytes alongside an error, want \"\"", p, len(got))
		}
	}
}

func TestHiddenDeepNesting(t *testing.T) {
	const depth = 100
	pattern := strings.Repeat("1[", depth) + "ab" + strings.Repeat("]", depth)
	if got, err := Expand(pattern); err != nil || got != "ab" {
		t.Fatalf("depth %d with count 1: got %q, %v", depth, got, err)
	}
	pattern = strings.Repeat("2[", depth) + "a" + strings.Repeat("]", depth)
	if got, err := Expand(pattern); err == nil {
		t.Fatalf("2^%d expansion must fail, got %d bytes", depth, len(got))
	}
	pattern = strings.Repeat("2[", 20) + "a" + strings.Repeat("]", 20)
	if got, err := Expand(pattern); err != nil || len(got) != 1<<20 || strings.Count(got, "a") != 1<<20 {
		t.Fatalf("2^20 expansion: got %d bytes, %v", len(got), err)
	}
}

// hiddenGenerate builds a random valid pattern and its expansion the slow way.
func hiddenGenerate(rng *rand.Rand, depth int) (pattern, text string) {
	for items := rng.Intn(4); items > 0; items-- {
		if depth > 0 && rng.Intn(3) == 0 {
			count := rng.Intn(4) + 1
			inner, innerText := hiddenGenerate(rng, depth-1)
			if inner == "" {
				inner, innerText = "q", "q"
			}
			pattern += strconv.Itoa(count) + "[" + inner + "]"
			text += strings.Repeat(innerText, count)
		} else {
			c := string(rune('a' + rng.Intn(4)))
			pattern += c
			text += c
		}
	}
	return pattern, text
}

func TestHiddenAgainstGenerator(t *testing.T) {
	rng := rand.New(rand.NewSource(394))
	for round := 0; round < 1000; round++ {
		pattern, want := hiddenGenerate(rng, 3)
		got, err := Expand(pattern)
		if err != nil {
			t.Fatalf("round %d: Expand(%q) returned error %v", round, pattern, err)
		}
		if got != want {
			t.Fatalf("round %d: Expand(%q) = %q, want %q", round, pattern, got, want)
		}
	}
}

func TestHiddenLargeExpansions(t *testing.T) {
	run := func(pattern string) (string, error) {
		type result struct {
			text string
			err  error
		}
		done := make(chan result, 1)
		go func() {
			text, err := Expand(pattern)
			done <- result{text, err}
		}()
		select {
		case r := <-done:
			return r.text, r.err
		case <-time.After(10 * time.Second):
			t.Fatalf("Expand(%q) took longer than 10s", pattern)
			return "", nil
		}
	}
	nested := "10[10[10[10[10[10[a]]]]]]"
	if got, err := run(nested); err != nil || len(got) != 1_000_000 || strings.Count(got, "a") != 1_000_000 {
		t.Fatalf("nested: got %d bytes, %v", len(got), err)
	}
	if got, err := run("1000000[a]"); err != nil || got != strings.Repeat("a", 1_000_000) {
		t.Fatalf("flat: got %d bytes, %v", len(got), err)
	}
	mixed := "1000[ab10[cd]e2[f]]"
	if got, err := run(mixed); err != nil || got != strings.Repeat("ab"+strings.Repeat("cd", 10)+"eff", 1000) {
		t.Fatalf("mixed: got %d bytes, %v", len(got), err)
	}
	if got, err := run("1000[1000[1000[1000[a]]]]"); err == nil {
		t.Fatalf("oversized: got %d bytes, want an error", len(got))
	}
}
