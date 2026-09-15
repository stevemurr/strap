package templates

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		template string
		want     bool
	}{
		{"{{ user.name }}", true},
		{"[a](b){c}", true},
		{"{[}]", false},
		{"(((", false},
		{")(", false},
		{"plain text, no delimiters", true},
		{"", true},
		{"()", true},
		{"(", false},
		{")", false},
		{"]", false},
		{"}", false},
		{"([{}])", true},
		{"([)]", false},
		{"{a[b(c)d]e}f", true},
		{"(()", false},
		{"())", false},
		{"<>", true},
		{"{{#each items}}[{{.}}]{{/each}}", true},
		{"()[]{}", true},
		{"(]", false},
		{"{}}", false},
		{"héllo (wörld)", true},
		{"\n\t(\n)\n", true},
	}
	for _, c := range cases {
		if got := Balanced(c.template); got != c.want {
			t.Errorf("Balanced(%q) = %v, want %v", c.template, got, c.want)
		}
	}
}

func hiddenBrute(template string) bool {
	var kept strings.Builder
	for i := 0; i < len(template); i++ {
		if strings.IndexByte("()[]{}", template[i]) >= 0 {
			kept.WriteByte(template[i])
		}
	}
	s := kept.String()
	for {
		next := s
		for _, pair := range []string{"()", "[]", "{}"} {
			next = strings.ReplaceAll(next, pair, "")
		}
		if next == s {
			return s == ""
		}
		s = next
	}
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(20))
	const alphabet = "()[]{}ab "
	var build func(depth int) string
	build = func(depth int) string {
		var sb strings.Builder
		for parts := rng.Intn(4); parts > 0; parts-- {
			if depth == 0 || rng.Intn(3) == 0 {
				sb.WriteByte(alphabet[6+rng.Intn(3)])
				continue
			}
			p := rng.Intn(3)
			sb.WriteByte("([{"[p])
			sb.WriteString(build(depth - 1))
			sb.WriteByte(")]}"[p])
		}
		return sb.String()
	}
	for round := 0; round < 600; round++ {
		var s string
		if round%2 == 0 {
			b := make([]byte, rng.Intn(12))
			for i := range b {
				b[i] = alphabet[rng.Intn(len(alphabet))]
			}
			s = string(b)
		} else {
			s = build(3)
			if len(s) > 1 && rng.Intn(2) == 0 {
				b := []byte(s)
				i, j := rng.Intn(len(b)), rng.Intn(len(b))
				b[i], b[j] = b[j], b[i]
				s = string(b)
			}
		}
		if got, want := Balanced(s), hiddenBrute(s); got != want {
			t.Fatalf("round %d: Balanced(%q) = %v, want %v", round, s, got, want)
		}
	}
}

func TestHiddenLongTemplates(t *testing.T) {
	const n = 1_000_000
	cases := []struct {
		name     string
		template string
		want     bool
	}{
		{"deep nesting", strings.Repeat("(", n/2) + strings.Repeat(")", n/2), true},
		{"mixed deep nesting", strings.Repeat("([{", n/6) + strings.Repeat("}])", n/6), true},
		{"flat pairs", strings.Repeat("{}", n/2), true},
		{"one closer short", strings.Repeat("[", n/2) + strings.Repeat("]", n/2-1), false},
		{"wrong closer at the end", strings.Repeat("(", n/2) + strings.Repeat(")", n/2-1) + "]", false},
		{"never closed", strings.Repeat("{", n), false},
		{"prose with a few delimiters", strings.Repeat("lorem ipsum ", n/12) + "({[]})", true},
	}
	for _, c := range cases {
		done := make(chan bool, 1)
		go func() { done <- Balanced(c.template) }()
		select {
		case got := <-done:
			if got != c.want {
				t.Errorf("%s: Balanced = %v, want %v", c.name, got, c.want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: Balanced took longer than 10s on %d bytes", c.name, len(c.template))
		}
	}
}
