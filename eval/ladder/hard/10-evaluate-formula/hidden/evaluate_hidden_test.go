package formulas

import (
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		expr string
		want int
	}{
		{"1 + 2 * 3", 7},
		{"(1 + 2) * 3", 9},
		{"8 / 4 / 2", 1},
		{"-7 / 2", -3},
		{"2 * -3", -6},
		{"2 - (3 - 4)", 3},
		{"--5", 5},
		{"10 - 2 - 3", 5},
		{"42", 42},
		{"  42  ", 42},
		{"007", 7},
		{"0", 0},
		{"-0", 0},
		{"1-1", 0},
		{"2*(3+4)*5", 70},
		{"100/7", 14},
		{"-100/7", -14},
		{"100/-7", -14},
		{"(-8)/3", -2},
		{"7 / -2", -3},
		{"1 - -2", 3},
		{"3 - - - 3", 0},
		{"((((7))))", 7},
		{"2*3/4", 1},
		{"2/4*3", 0},
		{"7 - 2 * 3", 1},
		{"-2 * -3", 6},
		{"10 / 3 * 3", 9},
		{"-(2 + 3) * 2", -10},
		{"(2+3)*(4-1)/5", 3},
		{"1+2+3+4+5+6+7+8+9+10", 55},
		{"1000000 * 1000000", 1_000_000_000_000},
		{"( 1 ) + ( 2 )", 3},
		{"0 * 5 / 3", 0},
		{"-(-(-1))", -1},
		{"12+34", 46},
	}
	for _, c := range cases {
		got, err := Evaluate(c.expr)
		if err != nil || got != c.want {
			t.Errorf("Evaluate(%q) = %d, %v; want %d, nil", c.expr, got, err, c.want)
		}
	}
}

func TestHiddenErrors(t *testing.T) {
	cases := []string{
		"",
		" ",
		"   ",
		"+1",
		"1 +",
		"1 2",
		"()",
		"( )",
		"(1 + 2",
		"1 + 2)",
		"1 */ 2",
		"2 x 3",
		"1.5",
		"1\t+2",
		"1\n",
		"12 34",
		"(",
		")",
		"-",
		"--",
		"1 -",
		"*1",
		"/1",
		"1 + (2 *) 3",
		"abc",
		"1//2",
		"1 / 0",
		"4 / (2 - 2)",
		"0 / 0",
		"(1 / 0) * 0",
		"1 + 2 / (3 - 3) + 4",
		"1 +- ",
		"(1)(2)",
		"1 (2)",
		"2(3)",
		"1e3",
		"0x10",
		"1,000",
		"1 + +2",
		"1 * * 2",
		")(",
		"((1)",
		"(1))",
		"3 % 2",
		"2 ** 3",
	}
	for _, expr := range cases {
		if got, err := Evaluate(expr); err == nil {
			t.Errorf("Evaluate(%q) = %d, nil; want an error", expr, got)
		}
	}
}

// hiddenGen builds random formulas straight from the grammar and computes their
// value alongside, so it doubles as the oracle. divZero records that some
// division by zero was generated, in which case an error is expected.
type hiddenGen struct {
	rng     *rand.Rand
	divZero bool
}

func (g *hiddenGen) sp() string {
	return strings.Repeat(" ", g.rng.Intn(3))
}

func (g *hiddenGen) expr(depth int) (string, int) {
	s, v := g.term(depth)
	for k := g.rng.Intn(3); k > 0; k-- {
		t, tv := g.term(depth)
		if g.rng.Intn(2) == 0 {
			s += g.sp() + "+" + g.sp() + t
			v += tv
		} else {
			s += g.sp() + "-" + g.sp() + t
			v -= tv
		}
	}
	return s, v
}

func (g *hiddenGen) term(depth int) (string, int) {
	s, v := g.factor(depth)
	for k := g.rng.Intn(2); k > 0; k-- {
		f, fv := g.factor(depth)
		if g.rng.Intn(2) == 0 {
			s += g.sp() + "*" + g.sp() + f
			v *= fv
		} else {
			s += g.sp() + "/" + g.sp() + f
			if fv == 0 {
				g.divZero = true
			} else {
				v /= fv
			}
		}
	}
	return s, v
}

func (g *hiddenGen) factor(depth int) (string, int) {
	choice := g.rng.Intn(10)
	switch {
	case depth > 0 && choice < 2:
		f, fv := g.factor(depth - 1)
		return "-" + g.sp() + f, -fv
	case depth > 0 && choice < 5:
		e, ev := g.expr(depth - 1)
		return "(" + g.sp() + e + g.sp() + ")", ev
	}
	n := g.rng.Intn(10)
	lit := strconv.Itoa(n)
	if g.rng.Intn(8) == 0 {
		lit = "0" + lit
	}
	return lit, n
}

func TestHiddenAgainstGrammarOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(224))
	for round := 0; round < 2000; round++ {
		g := &hiddenGen{rng: rng}
		expr, want := g.expr(2)
		expr = g.sp() + expr + g.sp()
		got, err := Evaluate(expr)
		if g.divZero {
			if err == nil {
				t.Fatalf("round %d: Evaluate(%q) = %d, nil; want a division-by-zero error", round, expr, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Fatalf("round %d: Evaluate(%q) = %d, %v; want %d, nil", round, expr, got, err, want)
		}
	}
}

func TestHiddenDeepAndLong(t *testing.T) {
	const depth = 100_000
	const terms = 1_000_000
	cases := []struct {
		name string
		expr string
		want int
	}{
		{"nested parentheses", strings.Repeat("(", depth) + "7" + strings.Repeat(")", depth), 7},
		{"nested unary minus", strings.Repeat("-", depth) + "5", 5},
		{"nested minus parentheses", strings.Repeat("-(", depth) + "3" + strings.Repeat(")", depth), 3},
		{"long sum", strings.Repeat("1+", terms-1) + "1", terms},
		{"long alternating", strings.Repeat("2-1+", terms/2) + "0", terms / 2},
		{"long product chain", strings.Repeat("3*2/6*", terms/3) + "1", 1},
	}
	type answer struct {
		value int
		err   error
	}
	for _, c := range cases {
		done := make(chan answer, 1)
		go func() {
			v, err := Evaluate(c.expr)
			done <- answer{v, err}
		}()
		select {
		case a := <-done:
			if a.err != nil || a.value != c.want {
				t.Fatalf("%s: Evaluate = %d, %v; want %d, nil", c.name, a.value, a.err, c.want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: Evaluate took longer than 10s on %d bytes", c.name, len(c.expr))
		}
	}
}
