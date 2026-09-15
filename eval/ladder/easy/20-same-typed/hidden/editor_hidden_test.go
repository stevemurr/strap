package editor

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"ab#c", "ad#c", true},
		{"ab##", "c#d#", true},
		{"a#c", "b", false},
		{"a##b", "#b", true},
		{"abc", "abc#", false},
		{"", "", true},
		{"###", "", true},
		{"", "#", true},
		{"a#", "", true},
		{"a", "", false},
		{"", "a", false},
		{"a", "a", true},
		{"a", "b", false},
		{"ab#", "a", true},
		{"a", "a#a", true},
		{"bxj##tw", "bxo#j##tw", true},
		{"y#fo##f", "y#f#o##f", true},
		{"abc#d", "abd", true},
		{"ab", "ba", false},
		{"a#b#c#", "###", true},
		{"xy#z", "xzz#", true},
		{"aaa", "aa", false},
		{"aa#a", "aaa#", true},
		{"ab#c#", "a", true},
		{"a###b", "b", true},
		{"abc", "ab", false},
	}
	for _, c := range cases {
		if got := SameTyped(c.a, c.b); got != c.want {
			t.Errorf("SameTyped(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func replay(log string) string {
	var text []byte
	for i := 0; i < len(log); i++ {
		if log[i] == '#' {
			if len(text) > 0 {
				text = text[:len(text)-1]
			}
			continue
		}
		text = append(text, log[i])
	}
	return string(text)
}

func brute(a, b string) bool {
	return replay(a) == replay(b)
}

func randomLog(rng *rand.Rand, n int) string {
	alphabet := []byte("ab#")
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = alphabet[rng.Intn(len(alphabet))]
	}
	return string(buf)
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(844))
	for round := 0; round < 3000; round++ {
		a := randomLog(rng, rng.Intn(9))
		var b string
		if round%2 == 0 {
			b = randomLog(rng, rng.Intn(9))
		} else {
			// Insert a typed-then-deleted letter somewhere in a, so the logs
			// differ but usually produce the same text.
			at := rng.Intn(len(a) + 1)
			b = a[:at] + string(rune('a'+rng.Intn(3))) + "#" + a[at:]
			if rng.Intn(4) == 0 {
				b += "#"
			}
		}
		if got, want := SameTyped(a, b), brute(a, b); got != want {
			t.Fatalf("round %d: SameTyped(%q, %q) = %v, want %v", round, a, b, got, want)
		}
	}
}

func TestHiddenLongLogs(t *testing.T) {
	const reps = 333_333
	typed := strings.Repeat("ab#", reps)   // reps a's
	retyped := strings.Repeat("a#a", reps) // reps a's
	lastDiffers := strings.Repeat("a#a", reps-1) + "a#b"
	erased := strings.Repeat("q", 500_000) + strings.Repeat("#", 500_000)
	onlyBackspaces := strings.Repeat("#", 1_000_000)
	run := func(name, a, b string, want bool) {
		done := make(chan bool, 1)
		go func() { done <- SameTyped(a, b) }()
		select {
		case got := <-done:
			if got != want {
				t.Fatalf("%s: SameTyped on %d and %d bytes = %v, want %v", name, len(a), len(b), got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: SameTyped took longer than 10s on %d and %d bytes", name, len(a), len(b))
		}
	}
	run("same text", typed, retyped, true)
	run("last letter differs", typed, lastDiffers, false)
	run("everything erased", erased, onlyBackspaces, true)
	run("erased vs one letter", erased, "a", false)
}
