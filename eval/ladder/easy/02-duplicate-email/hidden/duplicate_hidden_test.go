package contacts

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name   string
		emails []string
		want   string
		ok     bool
	}{
		{"readme normalized", []string{"ann@shop.io", "bo@shop.io", " Ann@Shop.io "}, "ann@shop.io", true},
		{"readme first repeat wins", []string{"a@x.com", "b@x.com", "b@x.com", "a@x.com"}, "b@x.com", true},
		{"readme none", []string{"a@x.com", "b@x.com"}, "", false},
		{"readme exact repeat", []string{"ann@shop.io", "ann@shop.io"}, "ann@shop.io", true},
		{"readme blank pair", []string{"", " "}, "", true},
		{"readme empty", nil, "", false},
		{"single", []string{"solo@x.com"}, "", false},
		{"single blank", []string{"   "}, "", false},
		{"case only", []string{"A@X.COM", "a@x.com"}, "a@x.com", true},
		{"tabs and spaces", []string{"\tuser@x.com  ", "user@x.com"}, "user@x.com", true},
		{"interior space is significant", []string{"a b@x.com", "ab@x.com"}, "", false},
		{"later duplicate", []string{"a@x", "b@x", "c@x", "d@x", "C@X"}, "c@x", true},
		{"three copies report first repeat", []string{"z@x", "q@x", "z@x", "z@x"}, "z@x", true},
		{"blank repeats later", []string{"", "a@x", ""}, "", true},
	}
	for _, c := range cases {
		got, ok := FirstDuplicate(c.emails)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: FirstDuplicate(%q) = (%q, %v), want (%q, %v)", c.name, c.emails, got, ok, c.want, c.ok)
		}
	}
}

func TestHiddenReturnsNormalizedForm(t *testing.T) {
	cases := []struct {
		emails []string
		want   string
	}{
		{[]string{" MIXED@Case.com", "mixed@CASE.com "}, "mixed@case.com"},
		{[]string{"user@x", "USER@X"}, "user@x"},
		{[]string{"USER@X", "user@x"}, "user@x"},
		{[]string{"  pad@x  ", "pad@x"}, "pad@x"},
	}
	for _, c := range cases {
		got, ok := FirstDuplicate(c.emails)
		if !ok || got != c.want {
			t.Errorf("FirstDuplicate(%q) = (%q, %v), want (%q, true)", c.emails, got, ok, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	emails := []string{" A@x ", "b@X", "a@X"}
	before := append([]string(nil), emails...)
	FirstDuplicate(emails)
	if !reflect.DeepEqual(emails, before) {
		t.Fatalf("input was modified: %q", emails)
	}
}

func brute(emails []string) (string, bool) {
	norm := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	for j := range emails {
		for i := 0; i < j; i++ {
			if norm(emails[i]) == norm(emails[j]) {
				return norm(emails[j]), true
			}
		}
	}
	return "", false
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(217))
	users := []string{"ann", "bo", "cy", "di", "eve"}
	domains := []string{"x.io", "y.io"}
	pads := []string{"", " ", "\t", "  "}
	for round := 0; round < 500; round++ {
		n := rng.Intn(25)
		emails := make([]string, n)
		for i := range emails {
			addr := users[rng.Intn(len(users))] + "@" + domains[rng.Intn(len(domains))]
			switch rng.Intn(3) {
			case 0:
				addr = strings.ToUpper(addr)
			case 1:
				addr = strings.ToUpper(addr[:1]) + addr[1:]
			}
			emails[i] = pads[rng.Intn(len(pads))] + addr + pads[rng.Intn(len(pads))]
		}
		got, ok := FirstDuplicate(emails)
		want, wantOK := brute(emails)
		if got != want || ok != wantOK {
			t.Fatalf("round %d: FirstDuplicate(%q) = (%q, %v), want (%q, %v)", round, emails, got, ok, want, wantOK)
		}
	}
}

func TestHiddenLargeImport(t *testing.T) {
	const n = 1_000_000
	emails := make([]string, n)
	for i := range emails {
		emails[i] = fmt.Sprintf("user%d@example.com", i)
	}
	type answer struct {
		addr string
		ok   bool
	}
	run := func(name string) answer {
		done := make(chan answer, 1)
		go func() {
			addr, ok := FirstDuplicate(emails)
			done <- answer{addr, ok}
		}()
		select {
		case a := <-done:
			return a
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: FirstDuplicate took longer than 10s on %d emails", name, n)
			return answer{}
		}
	}
	if a := run("all distinct"); a.ok || a.addr != "" {
		t.Fatalf("all distinct: got %+v", a)
	}
	emails[n-1] = " USER0@Example.com"
	if a := run("last repeats first"); !a.ok || a.addr != "user0@example.com" {
		t.Fatalf("last repeats first: got %+v", a)
	}
}
