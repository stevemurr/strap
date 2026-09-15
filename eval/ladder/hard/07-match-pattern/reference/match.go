// Package globs matches file names against the wildcard patterns used by
// deploy manifests.
package globs

// Match walks pattern and name together. A star is first assumed to match
// nothing and its position is remembered; on a later mismatch the scan
// returns to that star, lets it absorb one more byte of name, and continues.
// Only the most recent star needs remembering, because any earlier star could
// equally absorb the bytes an earlier retry would give it, so each name byte
// is revisited at most once per pattern byte after the last star.
func Match(pattern, name string) bool {
	p, n := 0, 0
	starP, starN := -1, 0
	for n < len(name) {
		switch {
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == name[n]):
			p++
			n++
		case p < len(pattern) && pattern[p] == '*':
			starP, starN = p, n
			p++
		case starP >= 0:
			starN++
			p, n = starP+1, starN
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}
