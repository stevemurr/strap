// Package slots prepares shift rosters for display.
package slots

// Compact writes each non-empty string to the next free position at the front
// of the slice, then clears everything after the last one written. The write
// index never overtakes the read index, so no value is lost.
func Compact(slots []string) {
	w := 0
	for _, s := range slots {
		if s != "" {
			slots[w] = s
			w++
		}
	}
	for ; w < len(slots); w++ {
		slots[w] = ""
	}
}
