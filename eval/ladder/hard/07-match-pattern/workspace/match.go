// Package globs matches file names against the wildcard patterns used by
// deploy manifests.
package globs

// Match reports whether name matches pattern in full. '?' matches any single
// byte and '*' matches any run of bytes, including none; every other byte
// matches itself. README.md defines the rules and the performance
// requirement.
func Match(pattern, name string) bool {
	panic("not implemented")
}
