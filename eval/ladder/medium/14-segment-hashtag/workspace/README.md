# Segment hashtag

The social feed turns hashtags like `#gonorthsummer` back into readable
phrases. Given the tag text and the dictionary of known words, split the text
into a sequence of dictionary words.

## Contract

Package `hashtags`, file `segment.go`:

```go
func Segment(text string, words []string) ([]string, bool)
```

- On success returns `parts, true` where every part is an element of `words`
  and `strings.Join(parts, "")` equals `text`. A word may be used any number of
  times. When several segmentations exist, any one of them is accepted.
- Returns `nil, false` when `text` cannot be written that way.
- An empty `text` is segmented into zero parts: returns a zero-length slice
  (`nil` is fine) and `true`.
- `words` never contains the empty string; it may contain duplicates and is in
  no particular order. Matching is exact and case-sensitive on bytes.
- Neither `text` nor `words` may be modified.

## Examples

| text | words | result |
|---|---|---|
| `"gonorthsummer"` | `["go", "north", "summer", "on"]` | `["go", "north", "summer"], true` |
| `"applepenapple"` | `["apple", "pen"]` | `["apple", "pen", "apple"], true` |
| `"catsandog"` | `["cats", "dog", "sand", "and", "cat"]` | `nil, false` |
| `"aaaa"` | `["a", "aa"]` | `["aa", "aa"], true` (any valid split is fine) |
| `""` | `["a"]` | `[], true` |
| `"a"` | `[]` | `nil, false` |

## Constraints

- `text` up to 100,000 bytes; up to 1,000 words of at most 20 bytes each.
- The hidden tests include a text of 10,000 `a`s followed by one `b` with the
  words `["a", "aa", "aaa"]`; the call must report `false` well within a
  second. Exhaustive backtracking is far too slow for that; remember which
  prefixes have already been decided.
- Standard library only. Keep the package name, file name and exported signature.
