# Minimum largest shard

The index builder splits an ordered list of document batches across a fixed
number of shards. Batches must stay in order and each shard takes a contiguous
run of them, so the only choice is where to cut. The build finishes when the
heaviest shard does, so choose the cuts that make the largest shard total as
small as possible.

## Contract

Package `sharding`, file `shard.go`:

```go
func MinLargestShard(sizes []int, shards int) int
```

- `sizes[i] >= 0` is the size of batch `i`. Split the batches, in order, into
  exactly `shards` contiguous, non-empty groups that together cover every
  batch. Return the smallest value that the largest group total can have.
- `shards >= len(sizes)` returns the largest single size (every batch on its
  own shard, with any surplus shards unused).
- `shards < 1` or an empty `sizes` returns `0`.
- The total of all sizes fits in an `int`.
- The input must not be modified.

## Examples

| sizes | shards | result |
|---|---|---|
| `[7, 2, 5, 10, 8]` | 2 | 18 |
| `[1, 2, 3, 4, 5]` | 2 | 9 |
| `[1, 4, 4]` | 3 | 4 |
| `[2, 3, 1, 2, 4, 3]` | 5 | 4 |
| `[5, 5]` | 1 | 10 |
| `[0, 0, 0]` | 2 | 0 |
| `[3]` | 7 | 3 |
| `[]` | 3 | 0 |

## Constraints

- Up to 1,000,000 batches with sizes up to 1,000,000 and any number of shards
  up to the number of batches. The call must finish well under a second at
  that size; a table over every batch and shard count, or trying every
  candidate for the answer one by one, is too slow.
- Standard library only. Keep the package name, file name and exported signature.
