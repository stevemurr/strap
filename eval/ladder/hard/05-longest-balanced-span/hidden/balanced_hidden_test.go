package markers

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		markers string
		want    int
	}{
		{"(()", 2},
		{")()())", 4},
		{"()(())", 6},
		{"()(()", 2},
		{"((((", 0},
		{"", 0},
		{"(", 0},
		{")", 0},
		{"()", 2},
		{")(", 0},
		{"))))", 0},
		{"()()", 4},
		{"(())", 4},
		{"(()))", 4},
		{"((())", 4},
		{"()(()()", 4},
		{"(()()(", 4},
		{")()(()))(", 6},
		{"(()(((()", 2},
		{"()(())(()", 6},
		{"))(()())((", 6},
	}
	for _, c := range cases {
		if got := LongestBalanced(c.markers); got != c.want {
			t.Errorf("LongestBalanced(%q) = %d, want %d", c.markers, got, c.want)
		}
	}
}

// wellFormed checks a substring with a running depth.
func wellFormed(s string) bool {
	depth := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '(' {
			depth++
		} else {
			depth--
		}
		if depth < 0 {
			return false
		}
	}
	return depth == 0
}

func brute(markers string) int {
	best := 0
	for i := 0; i < len(markers); i++ {
		for j := i + 2; j <= len(markers); j += 2 {
			if j-i > best && wellFormed(markers[i:j]) {
				best = j - i
			}
		}
	}
	return best
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(32))
	for round := 0; round < 600; round++ {
		n := rng.Intn(30)
		b := make([]byte, n)
		bias := []int{50, 40, 60}[round%3]
		for i := range b {
			if rng.Intn(100) < bias {
				b[i] = '('
			} else {
				b[i] = ')'
			}
		}
		s := string(b)
		if got, want := LongestBalanced(s), brute(s); got != want {
			t.Fatalf("round %d: LongestBalanced(%q) = %d, want %d", round, s, got, want)
		}
	}
}

func TestHiddenLargeInput(t *testing.T) {
	const n = 5_000_000
	shapes := []struct {
		name string
		data string
		want int
	}{
		{"nested", strings.Repeat("(", n/2) + strings.Repeat(")", n/2), n},
		{"alternating", strings.Repeat("()", n/2), n},
		{"stuck opener", strings.Repeat("(()", n/3), 2},
		{"stuck closer", strings.Repeat("())", n/3), 2},
		{"all open", strings.Repeat("(", n), 0},
		{"all close", strings.Repeat(")", n), 0},
		{"one short", strings.Repeat("(", n/2) + strings.Repeat(")", n/2-1) + "(", n - 2},
	}
	for _, shape := range shapes {
		done := make(chan int, 1)
		go func() { done <- LongestBalanced(shape.data) }()
		select {
		case got := <-done:
			if got != shape.want {
				t.Fatalf("%s: LongestBalanced = %d, want %d", shape.name, got, shape.want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: LongestBalanced took longer than 10s on %d bytes", shape.name, len(shape.data))
		}
	}
}
