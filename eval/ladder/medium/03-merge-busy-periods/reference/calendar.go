// Package calendar combines busy periods from several calendars into a
// free/busy view.
package calendar

import (
	"cmp"
	"slices"
)

// MergeBusy sorts a copy of the periods by start and sweeps through them once,
// extending the current block while the next period starts no later than the
// block ends and emitting the block when a gap appears.
func MergeBusy(periods [][2]int) [][2]int {
	if len(periods) == 0 {
		return nil
	}
	sorted := slices.Clone(periods)
	slices.SortFunc(sorted, func(a, b [2]int) int {
		return cmp.Or(cmp.Compare(a[0], b[0]), cmp.Compare(a[1], b[1]))
	})
	out := make([][2]int, 0, len(sorted))
	current := sorted[0]
	for _, p := range sorted[1:] {
		if p[0] <= current[1] {
			current[1] = max(current[1], p[1])
			continue
		}
		out = append(out, current)
		current = p
	}
	return append(out, current)
}
