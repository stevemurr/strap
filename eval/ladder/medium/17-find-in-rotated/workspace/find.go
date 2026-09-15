// Package ringlog looks up event ids in the crash log's ring buffer.
package ringlog

// Find returns the index of target in ids, a strictly ascending sequence
// that has been rotated by an unknown offset, or -1 when it is absent.
// README.md defines the edge cases and the performance requirement.
func Find(ids []int, target int) int {
	panic("not implemented")
}
