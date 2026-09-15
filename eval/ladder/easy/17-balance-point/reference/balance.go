// Package balance locates the balance point of a row of parcels.
package balance

// Point computes the total once, then walks left to right keeping the running
// sum of the weights already passed; the right side is the total minus the
// left side minus the current parcel.
func Point(weights []int) int {
	total := 0
	for _, w := range weights {
		total += w
	}
	left := 0
	for i, w := range weights {
		if left == total-left-w {
			return i
		}
		left += w
	}
	return -1
}
