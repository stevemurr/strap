// Package ringlog looks up event ids in the crash log's ring buffer.
package ringlog

// Find binary-searches the rotated slice. At every step at least one half of
// [lo, hi] is properly sorted; if target lies inside that half's value range
// the search continues there, otherwise in the other half.
func Find(ids []int, target int) int {
	lo, hi := 0, len(ids)-1
	for lo <= hi {
		mid := lo + (hi-lo)/2
		switch {
		case ids[mid] == target:
			return mid
		case ids[lo] <= ids[mid]:
			if ids[lo] <= target && target < ids[mid] {
				hi = mid - 1
			} else {
				lo = mid + 1
			}
		default:
			if ids[mid] < target && target <= ids[hi] {
				lo = mid + 1
			} else {
				hi = mid - 1
			}
		}
	}
	return -1
}
