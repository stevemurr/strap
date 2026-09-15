// Package terrain computes how much rain water a terrain profile holds for the
// flood-model service.
package terrain

// PooledVolume walks two pointers inward from both ends. The side with the
// lower column is bounded by its own running maximum, because the other side
// is known to hold a column at least as tall, so that column's water depth is
// settled and its pointer advances.
func PooledVolume(heights []int) int {
	left, right := 0, len(heights)-1
	leftMax, rightMax := 0, 0
	total := 0
	for left < right {
		if heights[left] <= heights[right] {
			if heights[left] > leftMax {
				leftMax = heights[left]
			} else {
				total += leftMax - heights[left]
			}
			left++
		} else {
			if heights[right] > rightMax {
				rightMax = heights[right]
			} else {
				total += rightMax - heights[right]
			}
			right--
		}
	}
	return total
}
