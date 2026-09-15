// Package sharding balances ordered document batches across index shards.
package sharding

// MinLargestShard binary-searches the answer between the largest single size
// and the total. A limit is feasible when greedily filling shards up to the
// limit needs at most the allowed number of shards; splitting a shard further
// never raises the largest total, so extra shards are always usable.
func MinLargestShard(sizes []int, shards int) int {
	if shards < 1 || len(sizes) == 0 {
		return 0
	}
	lo, hi := 0, 0
	for _, s := range sizes {
		lo = max(lo, s)
		hi += s
	}
	if shards >= len(sizes) {
		return lo
	}
	for lo < hi {
		mid := lo + (hi-lo)/2
		if shardsNeeded(sizes, mid) <= shards {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo
}

// shardsNeeded counts the groups a greedy left-to-right cut produces when no
// group may exceed limit. Every size is at most limit.
func shardsNeeded(sizes []int, limit int) int {
	groups, total := 1, 0
	for _, s := range sizes {
		if total+s > limit {
			groups++
			total = s
		} else {
			total += s
		}
	}
	return groups
}
