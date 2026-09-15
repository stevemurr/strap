// Package fixtures expands compact repeat patterns into fixture payloads.
package fixtures

// Expand turns a pattern of lowercase letters and nested count[pattern]
// groups into its expanded text, or returns an error for an invalid pattern
// or an expansion over the size limit. README.md defines the grammar, the
// error cases and the limit.
func Expand(pattern string) (string, error) {
	panic("not implemented")
}
