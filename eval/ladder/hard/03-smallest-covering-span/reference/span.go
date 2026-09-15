// Package logspan finds the shortest stretch of an event log that contains a
// required mix of event types.
package logspan

// SmallestCoveringSpan slides a window over events. need holds, per required
// type, how many more occurrences the window still lacks (negative when it
// has spares), and missing counts the types whose need is still positive. The
// right end grows until nothing is missing, then the left end shrinks while
// that stays true; every window recorded while shrinking is the shortest one
// ending at that right end, and a strict comparison keeps the earliest start
// among equal lengths.
func SmallestCoveringSpan(events []string, required []string) (start, end int, ok bool) {
	if len(required) == 0 {
		return 0, 0, false
	}
	need := make(map[string]int, len(required))
	for _, r := range required {
		need[r]++
	}
	missing := len(need)
	best := -1
	left := 0
	for right, e := range events {
		if n, tracked := need[e]; tracked {
			need[e] = n - 1
			if n == 1 {
				missing--
			}
		}
		for missing == 0 {
			if best < 0 || right+1-left < best {
				best = right + 1 - left
				start, end = left, right+1
			}
			if n, tracked := need[events[left]]; tracked {
				need[events[left]] = n + 1
				if n == 0 {
					missing++
				}
			}
			left++
		}
	}
	return start, end, best >= 0
}
