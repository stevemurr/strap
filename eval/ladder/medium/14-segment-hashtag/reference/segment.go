// Package hashtags splits hashtag text back into dictionary words.
package hashtags

// Segment decides, for every prefix length, whether that prefix can be
// segmented, remembering where the last word of one solution starts. Each
// prefix is settled once by looking back at most maxLen bytes, so the work is
// linear in the text for a fixed dictionary.
func Segment(text string, words []string) ([]string, bool) {
	dict := make(map[string]struct{}, len(words))
	maxLen := 0
	for _, w := range words {
		if w == "" {
			continue
		}
		dict[w] = struct{}{}
		if len(w) > maxLen {
			maxLen = len(w)
		}
	}
	n := len(text)
	// start[i] is the start of the last word in a segmentation of text[:i],
	// or -1 when text[:i] has none. start[0] = 0 marks the empty prefix.
	start := make([]int, n+1)
	for i := range start {
		start[i] = -1
	}
	start[0] = 0
	for end := 1; end <= n; end++ {
		for l := 1; l <= maxLen && l <= end; l++ {
			if start[end-l] < 0 {
				continue
			}
			if _, ok := dict[text[end-l:end]]; ok {
				start[end] = end - l
				break
			}
		}
	}
	if start[n] < 0 {
		return nil, false
	}
	var parts []string
	for end := n; end > 0; end = start[end] {
		parts = append(parts, text[start[end]:end])
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	if parts == nil {
		parts = []string{}
	}
	return parts, true
}
