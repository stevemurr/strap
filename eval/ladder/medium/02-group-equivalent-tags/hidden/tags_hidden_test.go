package tags

import (
	"math/rand"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		name string
		tags []string
		want [][]string
	}{
		{"readme", []string{"eat", "tea", "tan", "ate", "nat", "bat"}, [][]string{{"ate", "eat", "tea"}, {"bat"}, {"nat", "tan"}}},
		{"duplicates", []string{"b", "a", "b"}, [][]string{{"a"}, {"b", "b"}}},
		{"prefix is not equivalent", []string{"ab", "abc", "ba"}, [][]string{{"ab", "ba"}, {"abc"}}},
		{"empty strings", []string{"", "x", ""}, [][]string{{"", ""}, {"x"}}},
		{"case sensitive", []string{"Ab", "ab", "bA"}, [][]string{{"Ab", "bA"}, {"ab"}}},
		{"single", []string{"solo"}, [][]string{{"solo"}}},
		{"all identical", []string{"aa", "aa", "aa"}, [][]string{{"aa", "aa", "aa"}}},
		{"multiplicity matters", []string{"abb", "aab", "bba", "baa"}, [][]string{{"aab", "baa"}, {"abb", "bba"}}},
		{"digits and symbols", []string{"21", "12", "3", "!?", "?!"}, [][]string{{"!?", "?!"}, {"12", "21"}, {"3"}}},
		{"groups sorted by first element", []string{"zz", "ba", "ab", "y"}, [][]string{{"ab", "ba"}, {"y"}, {"zz"}}},
	}
	for _, c := range cases {
		got := GroupEquivalent(c.tags)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: GroupEquivalent(%q) = %q, want %q", c.name, c.tags, got, c.want)
		}
	}
}

func TestHiddenEmpty(t *testing.T) {
	if got := GroupEquivalent(nil); len(got) != 0 {
		t.Fatalf("GroupEquivalent(nil) = %q, want empty", got)
	}
	if got := GroupEquivalent([]string{}); len(got) != 0 {
		t.Fatalf("GroupEquivalent([]) = %q, want empty", got)
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	tags := []string{"tea", "eat", "b", "a", "ate"}
	before := slices.Clone(tags)
	GroupEquivalent(tags)
	if !slices.Equal(tags, before) {
		t.Fatalf("input modified: %q", tags)
	}
}

// bruteGroups compares byte histograms pairwise instead of keying a map.
func bruteGroups(tags []string) [][]string {
	var groups [][]string
	var histograms [][256]int
	for _, tag := range tags {
		var h [256]int
		for i := 0; i < len(tag); i++ {
			h[tag[i]]++
		}
		placed := false
		for gi := range groups {
			if histograms[gi] == h {
				groups[gi] = append(groups[gi], tag)
				placed = true
				break
			}
		}
		if !placed {
			groups = append(groups, []string{tag})
			histograms = append(histograms, h)
		}
	}
	for _, g := range groups {
		sort.Strings(g)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0] < groups[j][0] })
	return groups
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(49))
	for round := 0; round < 400; round++ {
		alphabet := []string{"ab", "abc", "aB1"}[round%3]
		n := rng.Intn(30)
		tags := make([]string, n)
		for i := range tags {
			b := make([]byte, rng.Intn(5))
			for j := range b {
				b[j] = alphabet[rng.Intn(len(alphabet))]
			}
			tags[i] = string(b)
		}
		got, want := GroupEquivalent(tags), bruteGroups(tags)
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round %d: GroupEquivalent(%q) = %q, want %q", round, tags, got, want)
		}
	}
}

func TestHiddenLargeInput(t *testing.T) {
	const n = 200_000
	rng := rand.New(rand.NewSource(4949))
	shapes := []struct {
		name     string
		alphabet string
		minLen   int
	}{
		{"mostly distinct", "abcdefghijklmnopqrstuvwxyz", 5},
		{"heavy collisions", "abc", 1},
	}
	for _, shape := range shapes {
		tags := make([]string, n)
		for i := range tags {
			b := make([]byte, shape.minLen+rng.Intn(11-shape.minLen))
			for j := range b {
				b[j] = shape.alphabet[rng.Intn(len(shape.alphabet))]
			}
			tags[i] = string(b)
		}
		done := make(chan [][]string, 1)
		go func() { done <- GroupEquivalent(tags) }()
		var got [][]string
		select {
		case got = <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: GroupEquivalent took longer than 10s on %d tags", shape.name, n)
		}
		checkGrouping(t, shape.name, tags, got)
	}
}

// checkGrouping verifies a result without an oracle: every input tag is used
// exactly once, members of a group share a byte multiset, no multiset is split
// across groups, and both the groups and their members are in order.
func checkGrouping(t *testing.T, name string, tags []string, got [][]string) {
	t.Helper()
	remaining := make(map[string]int, len(tags))
	for _, tag := range tags {
		remaining[tag]++
	}
	seen := make(map[string]bool, len(got))
	total := 0
	for gi, group := range got {
		if len(group) == 0 {
			t.Fatalf("%s: group %d is empty", name, gi)
		}
		if gi > 0 && got[gi-1][0] >= group[0] {
			t.Fatalf("%s: groups %d and %d are not in ascending order (%q, %q)", name, gi-1, gi, got[gi-1][0], group[0])
		}
		key := sortedKey(group[0])
		if seen[key] {
			t.Fatalf("%s: equivalent tags split across groups (group %d)", name, gi)
		}
		seen[key] = true
		for i, member := range group {
			if i > 0 && group[i-1] > member {
				t.Fatalf("%s: group %d is not sorted at %d", name, gi, i)
			}
			if sortedKey(member) != key {
				t.Fatalf("%s: group %d mixes %q with %q", name, gi, group[0], member)
			}
			remaining[member]--
			if remaining[member] < 0 {
				t.Fatalf("%s: %q appears more often in the output than in the input", name, member)
			}
		}
		total += len(group)
	}
	if total != len(tags) {
		t.Fatalf("%s: output holds %d tags, want %d", name, total, len(tags))
	}
}

func sortedKey(s string) string {
	b := []byte(s)
	slices.Sort(b)
	return string(b)
}
