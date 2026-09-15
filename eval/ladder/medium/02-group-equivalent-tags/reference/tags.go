// Package tags groups user-entered tags that are rearrangements of each other.
package tags

import (
	"slices"
	"strings"
)

// GroupEquivalent keys every tag by its sorted bytes, so equivalent tags share
// a key, and buckets them in a map. Each bucket is then sorted and the buckets
// are ordered by their smallest member.
func GroupEquivalent(tags []string) [][]string {
	groups := make(map[string][]string)
	for _, tag := range tags {
		key := sortedBytes(tag)
		groups[key] = append(groups[key], tag)
	}
	out := make([][]string, 0, len(groups))
	for _, group := range groups {
		slices.Sort(group)
		out = append(out, group)
	}
	slices.SortFunc(out, func(a, b []string) int { return strings.Compare(a[0], b[0]) })
	return out
}

func sortedBytes(s string) string {
	b := []byte(s)
	slices.Sort(b)
	return string(b)
}
