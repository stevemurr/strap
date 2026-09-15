// Package collation reconstructs a partner's alphabet ordering from word lists
// sorted under it.
package collation

// InferOrder returns every letter that appears in sorted, each once, in an
// order under which the list is sorted, preferring the lexicographically
// smallest such order. It returns an error when no order works. README.md
// defines the rules, the tie-break and the performance requirement.
func InferOrder(sorted []string) (string, error) {
	panic("not implemented")
}
