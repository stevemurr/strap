# Justify lines

The receipt-printer driver lays out plain text in a fixed-width column. Given
the words of a paragraph and the column width, produce the printed lines: as
many words per line as fit, fully justified except for the last line.

## Contract

Package `textwrap`, file `justify.go`:

```go
func Justify(words []string, width int) []string
```

- Words are non-empty ASCII strings without spaces, and every word is at most
  `width` bytes long.
- Pack greedily: a line takes as many words as fit with at least one space
  between neighbours, so words `w1..wm` fit when
  `len(w1)+...+len(wm)+(m-1) <= width`. The first word that would not fit
  starts the next line.
- Every returned line is exactly `width` bytes long and never starts with a
  space.
- A fully justified line is any line except the last one that holds two or
  more words. Its spare spaces are shared among the gaps between words as
  evenly as possible; when they cannot be equal, the gaps on the left get one
  more space than the gaps on the right, so gap sizes never increase from left
  to right and differ by at most one.
- A left-justified line is the last line, and any line holding a single word.
  Its words are separated by exactly one space and the line is padded with
  spaces on the right.
- Returns a slice of length 0 for empty `words` (`nil` is acceptable).
- The input must not be modified.

## Examples

| words | width | result |
|---|---|---|
| `["This", "is", "an", "example", "of", "text", "justification."]` | 16 | `["This    is    an", "example  of text", "justification.  "]` |
| `["What", "must", "be", "acknowledgment", "shall", "be"]` | 16 | `["What   must   be", "acknowledgment  ", "shall be        "]` |
| `["a", "b", "c"]` | 3 | `["a b", "c  "]` |
| `["ab", "cd"]` | 2 | `["ab", "cd"]` |
| `["a"]` | 3 | `["a  "]` |
| `[]` | 5 | `[]` |

## Constraints

- Up to 200,000 words and widths up to 5,000, for a few megabytes of output
  in total. The call must finish well under a second at that size; growing
  the output by repeated string concatenation is too slow.
- Standard library only. Keep the package name, file name and exported signature.
