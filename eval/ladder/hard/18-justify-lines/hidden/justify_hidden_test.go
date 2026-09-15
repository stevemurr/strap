package textwrap

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name  string
		words []string
		width int
		want  []string
	}{
		{"readme", []string{"This", "is", "an", "example", "of", "text", "justification."}, 16,
			[]string{"This    is    an", "example  of text", "justification.  "}},
		{"single word line", []string{"What", "must", "be", "acknowledgment", "shall", "be"}, 16,
			[]string{"What   must   be", "acknowledgment  ", "shall be        "}},
		{"science", []string{"Science", "is", "what", "we", "understand", "well", "enough", "to", "explain", "to", "a", "computer.", "Art", "is", "everything", "else", "we", "do"}, 20,
			[]string{"Science  is  what we", "understand      well", "enough to explain to", "a  computer.  Art is", "everything  else  we", "do                  "}},
		{"tiny", []string{"a", "b", "c"}, 3, []string{"a b", "c  "}},
		{"exact fit", []string{"ab", "cd"}, 2, []string{"ab", "cd"}},
		{"one word", []string{"a"}, 3, []string{"a  "}},
		{"one word exact", []string{"abc"}, 3, []string{"abc"}},
		{"all on last line", []string{"to", "be", "or"}, 20, []string{"to be or            "}},
		{"uneven gaps", []string{"a", "b", "c", "d", "e", "f"}, 6, []string{"a  b c", "d e f "}},
		{"three gaps two spare", []string{"aa", "bb", "cc", "dd", "eeee"}, 13, []string{"aa  bb  cc dd", "eeee         "}},
	}
	for _, c := range cases {
		got := Justify(c.words, c.width)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Justify(%q, %d) = %q, want %q", c.name, c.words, c.width, got, c.want)
		}
	}
}

func TestHiddenEmpty(t *testing.T) {
	if got := Justify(nil, 5); len(got) != 0 {
		t.Fatalf("Justify(nil, 5) = %q, want empty", got)
	}
	if got := Justify([]string{}, 1); len(got) != 0 {
		t.Fatalf("Justify([], 1) = %q, want empty", got)
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	words := []string{"keep", "these", "words", "as", "they", "are"}
	before := append([]string(nil), words...)
	Justify(words, 9)
	if !reflect.DeepEqual(words, before) {
		t.Fatalf("input modified: %q", words)
	}
}

// hiddenCheck verifies every rule in README.md; together they determine the output
// uniquely, so a layout that passes is the only correct one.
func hiddenCheck(words []string, width int, lines []string) error {
	next := 0
	prevMinimal := -1 // minimal length of the previous line's words with single spaces
	for li, line := range lines {
		if len(line) != width {
			return fmt.Errorf("line %d has length %d, want %d", li, len(line), width)
		}
		if line[0] == ' ' {
			return fmt.Errorf("line %d starts with a space", li)
		}
		var lineWords []string
		var gaps []int
		trailing := 0
		for i := 0; i < len(line); {
			j := i
			for j < len(line) && line[j] != ' ' {
				j++
			}
			lineWords = append(lineWords, line[i:j])
			s := j
			for s < len(line) && line[s] == ' ' {
				s++
			}
			if s < len(line) {
				gaps = append(gaps, s-j)
			} else {
				trailing = s - j
			}
			i = s
		}
		minimal := len(lineWords) - 1
		for _, w := range lineWords {
			if next >= len(words) || words[next] != w {
				return fmt.Errorf("line %d: unexpected word %q", li, w)
			}
			next++
			minimal += len(w)
		}
		if li > 0 && prevMinimal+1+len(lineWords[0]) <= width {
			return fmt.Errorf("line %d: word %q would have fit on the previous line", li, lineWords[0])
		}
		prevMinimal = minimal
		if li == len(lines)-1 || len(lineWords) == 1 {
			for _, g := range gaps {
				if g != 1 {
					return fmt.Errorf("line %d: left-justified line has a gap of %d", li, g)
				}
			}
			continue
		}
		if trailing != 0 {
			return fmt.Errorf("line %d: fully justified line has %d trailing spaces", li, trailing)
		}
		for i := 1; i < len(gaps); i++ {
			if gaps[i] > gaps[i-1] {
				return fmt.Errorf("line %d: gap %d is wider than the gap before it", li, i)
			}
		}
		if gaps[0]-gaps[len(gaps)-1] > 1 {
			return fmt.Errorf("line %d: gaps differ by more than one", li)
		}
	}
	if next != len(words) {
		return fmt.Errorf("only %d of %d words were laid out", next, len(words))
	}
	return nil
}

func hiddenRandomWords(rng *rand.Rand, n, maxLen int) []string {
	words := make([]string, n)
	for i := range words {
		b := make([]byte, rng.Intn(maxLen)+1)
		for k := range b {
			b[k] = byte('a' + rng.Intn(26))
		}
		words[i] = string(b)
	}
	return words
}

func TestHiddenRandomLayouts(t *testing.T) {
	rng := rand.New(rand.NewSource(68))
	for round := 0; round < 500; round++ {
		maxLen := rng.Intn(6) + 1
		words := hiddenRandomWords(rng, rng.Intn(30)+1, maxLen)
		width := maxLen + rng.Intn(15)
		if err := hiddenCheck(words, width, Justify(words, width)); err != nil {
			t.Fatalf("round %d: Justify(%q, %d): %v", round, words, width, err)
		}
	}
}

func TestHiddenLargeParagraph(t *testing.T) {
	const n = 200_000
	rng := rand.New(rand.NewSource(5))
	prose := hiddenRandomWords(rng, n, 10)
	letters := hiddenRandomWords(rng, n, 1)
	for _, shape := range []struct {
		name  string
		words []string
		width int
	}{
		{"prose width 80", prose, 80},
		{"letters width 5000", letters, 5000},
	} {
		done := make(chan []string, 1)
		go func() { done <- Justify(shape.words, shape.width) }()
		var got []string
		select {
		case got = <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: Justify took longer than 10s on %d words", shape.name, n)
		}
		if err := hiddenCheck(shape.words, shape.width, got); err != nil {
			t.Fatalf("%s: %v", shape.name, err)
		}
	}
}
