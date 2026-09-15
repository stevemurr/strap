// Package tickets audits the support desk's ticket issue log.
package tickets

// Missing XORs every index and every logged number into one accumulator that
// starts at n. Each number that was issued cancels against its own value in
// 0..n, leaving only the missing one.
func Missing(issued []int) int {
	missing := len(issued)
	for i, v := range issued {
		missing ^= i ^ v
	}
	return missing
}
