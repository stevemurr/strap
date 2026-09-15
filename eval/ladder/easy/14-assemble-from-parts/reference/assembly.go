// Package assembly checks bills of parts against the kitting bin.
package assembly

// CanAssemble counts the parts in the bin once, then takes one unit off the
// count for every needed part; a count that is already zero means the bin is
// short of that part.
func CanAssemble(needed, available []string) bool {
	if len(needed) > len(available) {
		return false
	}
	counts := make(map[string]int, len(available))
	for _, part := range available {
		counts[part]++
	}
	for _, part := range needed {
		if counts[part] == 0 {
			return false
		}
		counts[part]--
	}
	return true
}
