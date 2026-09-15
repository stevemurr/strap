// Package receipts reconciles receipt ids from the terminal and acquirer feeds.
package receipts

// Unmatched XORs every id together. Each pair cancels itself out, so what
// remains is the id that appears once, in one pass with no extra memory.
func Unmatched(ids []int) int {
	x := 0
	for _, id := range ids {
		x ^= id
	}
	return x
}
