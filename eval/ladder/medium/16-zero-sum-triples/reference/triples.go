// Package reconcile finds groups of ledger deltas that cancel out.
package reconcile

import "sort"

// ZeroSumTriples sorts a copy of the deltas, fixes each distinct smallest
// value a, and walks two pointers inward over the remainder looking for
// b+c == -a. Skipping repeated values at every level yields each triple once,
// and the sorted scan produces them in ascending order.
func ZeroSumTriples(deltas []int) [][3]int {
	if len(deltas) < 3 {
		return nil
	}
	v := append([]int(nil), deltas...)
	sort.Ints(v)
	var out [][3]int
	for i := 0; i+2 < len(v); i++ {
		if v[i] > 0 {
			break
		}
		if i > 0 && v[i] == v[i-1] {
			continue
		}
		lo, hi := i+1, len(v)-1
		for lo < hi {
			sum := v[i] + v[lo] + v[hi]
			switch {
			case sum < 0:
				lo++
			case sum > 0:
				hi--
			default:
				out = append(out, [3]int{v[i], v[lo], v[hi]})
				for lo < hi && v[lo] == v[lo+1] {
					lo++
				}
				for lo < hi && v[hi] == v[hi-1] {
					hi--
				}
				lo++
				hi--
			}
		}
	}
	return out
}
