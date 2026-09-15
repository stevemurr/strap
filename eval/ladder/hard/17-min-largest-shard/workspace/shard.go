// Package sharding balances ordered document batches across index shards.
package sharding

// MinLargestShard returns the smallest possible total of the heaviest shard
// when sizes is cut, in order, into exactly shards contiguous non-empty groups.
// README.md defines the edge cases and the performance requirement.
func MinLargestShard(sizes []int, shards int) int {
	panic("not implemented")
}
