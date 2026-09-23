package work

import (
	"regexp"
	"strings"
	"testing"
)

// Ids carry a kind prefix and an opaque suffix. A model must not be able to
// derive the next id from the last one, so nothing in the suffix is sequential.
func TestIdsAreOpaqueUniqueAndKindPrefixed(t *testing.T) {
	shape := regexp.MustCompile(`^[a-z]+-[0-9a-z]{7}$`)
	s := New()
	seen := map[string]bool{}
	for i := 0; i < 20000; i++ {
		id := s.id("work")
		if !shape.MatchString(id) || !strings.HasPrefix(id, "work-") {
			t.Fatalf("unexpected id shape: %s", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = true
	}
	if len(s.issued) != 20000 {
		t.Fatalf("issued count %d", len(s.issued))
	}
	// Two stores must not issue the same sequence; the suffix is not a counter.
	a, b := New().id("plan"), New().id("plan")
	if a == b || strings.HasSuffix(a, "-0000001") {
		t.Fatalf("ids look sequential or shared: %s %s", a, b)
	}
	// Records keep their prefixes so the kind of an id stays readable.
	store, p, w := fixture(t)
	for prefix, id := range map[string]string{"plan": string(p.ID), "step": string(p.Steps[0].ID), "work": string(w.ID)} {
		if !strings.HasPrefix(id, prefix+"-") {
			t.Fatalf("%s id %s", prefix, id)
		}
	}
	if store.issued[string(w.ID)] != true {
		t.Fatal("issued ids are not tracked")
	}
}

// Execution refs are retyped by models from turns back, so they use the same
// short suffix as other ids and share the store's uniqueness check.
func TestExecutionRefsAreShortAndUnique(t *testing.T) {
	shape := regexp.MustCompile(`^execution:[0-9a-z]{7}$`)
	s := New()
	a, b := s.NewExecutionRef(), s.NewExecutionRef()
	if !shape.MatchString(a) || !shape.MatchString(b) || a == b || !s.issued[a] {
		t.Fatalf("execution refs %s %s", a, b)
	}
}
