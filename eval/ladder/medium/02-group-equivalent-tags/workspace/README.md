# Group equivalent tags

The tagging service normalises user-entered tags before search. Tags that are
rearrangements of each other ("listen", "silent", "enlist") should land in the
same bucket so a search for one of them finds the others. Given the raw tags,
group them by the bytes they are made of.

## Contract

Package `tags`, file `tags.go`:

```go
func GroupEquivalent(tags []string) [][]string
```

- Two tags are equivalent when they consist of the same bytes with the same
  multiplicities, that is, one is a rearrangement of the other. Comparison is
  byte-wise and case-sensitive: `"Ab"` and `"bA"` are equivalent, `"ab"` and
  `"AB"` are not.
- Each group holds every input tag equivalent to its members, sorted in
  ascending byte order. Duplicate input tags stay as duplicates inside their
  group, so the total number of strings across all groups equals `len(tags)`.
- Groups are ordered by their first (smallest) element in ascending byte order.
- The empty string is a tag like any other; all empty tags form one group.
- Empty input returns a zero-length result (nil is fine).
- The input slice must not be modified.

## Examples

| tags | result |
|---|---|
| `["eat", "tea", "tan", "ate", "nat", "bat"]` | `[["ate", "eat", "tea"], ["bat"], ["nat", "tan"]]` |
| `["b", "a", "b"]` | `[["a"], ["b", "b"]]` |
| `["ab", "abc", "ba"]` | `[["ab", "ba"], ["abc"]]` |
| `["", "x", ""]` | `[["", ""], ["x"]]` |
| `["Ab", "ab", "bA"]` | `[["Ab", "bA"], ["ab"]]` |
| `[]` | `[]` |

## Constraints

- Up to 200,000 tags, each up to 10 bytes long, most of them distinct. The call
  must finish well under a second at that size; comparing every tag against
  every group found so far is too slow.
- Standard library only. Keep the package name, file name and exported signature.
