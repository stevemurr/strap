// Package markers analyses the bracket structure of templates for the linter.
package markers

// LongestBalanced counts opening and closing brackets in one pass from the
// left: whenever the counts are equal the run since the last reset is
// well-formed, and a surplus of closers resets the run. That pass misses runs
// with a surplus of openers on their left, so a mirrored pass from the right
// catches those.
func LongestBalanced(markers string) int {
	best := 0
	opens, closes := 0, 0
	for i := 0; i < len(markers); i++ {
		if markers[i] == '(' {
			opens++
		} else {
			closes++
		}
		if opens == closes && 2*opens > best {
			best = 2 * opens
		}
		if closes > opens {
			opens, closes = 0, 0
		}
	}
	opens, closes = 0, 0
	for i := len(markers) - 1; i >= 0; i-- {
		if markers[i] == ')' {
			closes++
		} else {
			opens++
		}
		if opens == closes && 2*opens > best {
			best = 2 * opens
		}
		if opens > closes {
			opens, closes = 0, 0
		}
	}
	return best
}
