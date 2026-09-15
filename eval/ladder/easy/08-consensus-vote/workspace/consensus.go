// Package voting reconciles values reported by replicas.
package voting

// Consensus returns the value reported by a strict majority of votes and true,
// or "" and false when no value has a majority. README.md defines the edge
// cases.
func Consensus(votes []string) (string, bool) {
	panic("not implemented")
}
