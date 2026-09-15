// Package textwrap lays out paragraphs for the fixed-width receipt printer.
package textwrap

import "strings"

// Justify takes the longest prefix of the remaining words that fits, then
// either spreads the spare spaces over the gaps (leftmost gaps first) or, for
// the last line and single-word lines, joins with single spaces and pads on
// the right. Each line is built once in a strings.Builder.
func Justify(words []string, width int) []string {
	lines := make([]string, 0)
	var b strings.Builder
	for i := 0; i < len(words); {
		j, letters := i, 0
		for j < len(words) && letters+len(words[j])+(j-i) <= width {
			letters += len(words[j])
			j++
		}
		if j == i {
			j++ // A word longer than width; the contract rules this out.
		}
		b.Reset()
		b.Grow(width)
		gaps := j - i - 1
		if j == len(words) || gaps == 0 {
			for k := i; k < j; k++ {
				if k > i {
					b.WriteByte(' ')
				}
				b.WriteString(words[k])
			}
			b.WriteString(strings.Repeat(" ", max(0, width-b.Len())))
		} else {
			spare := width - letters
			each, extra := spare/gaps, spare%gaps
			for k := i; k < j; k++ {
				b.WriteString(words[k])
				if k < j-1 {
					n := each
					if k-i < extra {
						n++
					}
					b.WriteString(strings.Repeat(" ", n))
				}
			}
		}
		lines = append(lines, b.String())
		i = j
	}
	return lines
}
