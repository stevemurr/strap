// Package recipes compares recipe submissions for the deduplicator.
package recipes

// SameIngredients counts every name in a, then consumes the counts with the
// names in b. Because the lengths are equal, running out of a count for any
// name in b is the only way the multisets can differ.
func SameIngredients(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, name := range a {
		counts[name]++
	}
	for _, name := range b {
		if counts[name] == 0 {
			return false
		}
		counts[name]--
	}
	return true
}
