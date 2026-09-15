// Package voting reconciles values reported by replicas.
package voting

// Consensus runs Boyer-Moore majority voting: a candidate gains a point for
// each matching vote and loses one for each other vote, and is replaced when
// its count hits zero. Any strict majority survives that process, so a second
// pass only has to confirm the candidate really holds more than half.
func Consensus(votes []string) (string, bool) {
	candidate, count := "", 0
	for _, v := range votes {
		switch {
		case count == 0:
			candidate, count = v, 1
		case v == candidate:
			count++
		default:
			count--
		}
	}
	total := 0
	for _, v := range votes {
		if v == candidate {
			total++
		}
	}
	if total > len(votes)/2 {
		return candidate, true
	}
	return "", false
}
