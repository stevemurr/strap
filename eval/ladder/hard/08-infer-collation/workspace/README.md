# Infer collation

A partner sends us reference lists sorted under their own ordering of the
alphabet rather than ours. Given one such list, reconstruct an ordering of the
letters that explains it, so we can sort our own data the same way.

## Contract

Package `collation`, file `order.go`:

```go
func InferOrder(sorted []string) (string, error)
```

- Words contain only lowercase ASCII letters `a`-`z`; the hidden tests never
  pass anything else. A word may be empty, and words may repeat.
- The list is sorted under an unknown total order of the letters using the
  usual dictionary rule: two words compare at the first position where they
  differ; if one word runs out first, the shorter word comes first. Equal
  adjacent words say nothing.
- Return a string containing every letter that appears in any word, each
  exactly once, in an order under which the list is sorted. Letters that never
  appear are left out.
- When more than one order works, return the one that is smallest in ordinary
  `a`-`z` lexicographic comparison. Equivalently: repeatedly emit the
  `a`-`z` smallest letter all of whose required predecessors have already
  been emitted.
- Return `""` and a non-nil error when no order works: either the constraints
  contradict each other (for example `z` before `x` in one place and `x`
  before `z` in another, directly or through other letters), or a word is
  listed before its own proper prefix, such as `abc` before `ab`.
- An empty list returns `""` and no error. The input must not be modified.

## Examples

| sorted | result |
|---|---|
| `["wrt", "wrf", "er", "ett", "rftt"]` | `"wertf"` |
| `["dc", "db", "a"]` | `"cbda"` |
| `["bca"]` | `"abc"` |
| `["ab", "abc"]` | `"abc"` |
| `["z", "x", "z"]` | error |
| `["abc", "ab"]` | error |
| `[]` | `""` |

## Constraints

- Up to 100,000 words of up to 20 letters each. The call must finish in well
  under a second at that size; comparing every pair of words is far too slow,
  since adjacent pairs already carry all the information.
- Standard library only. Keep the package name, file name and exported signature.
