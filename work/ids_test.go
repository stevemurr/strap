package work

import "testing"

func TestSeededIDsAreAFunctionOfSeedAndOrder(t *testing.T) {
	issue := func(seed string) []string {
		s := New(WithIDs(SeededIDs([]byte(seed))))
		return []string{s.id("work"), s.id("plan"), s.id("work"), s.mint("execution:")}
	}
	a, b, c := issue("one"), issue("one"), issue("two")
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("same seed issued %v then %v", a, b)
		}
		if a[i] == c[i] {
			t.Fatalf("different seeds issued %s at %d", a[i], i)
		}
	}
	if a[0] == a[2] || len(a[0]) != len("work-")+idLength {
		t.Fatalf("ids %v", a)
	}
}
