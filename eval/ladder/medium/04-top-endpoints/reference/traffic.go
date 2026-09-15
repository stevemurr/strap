// Package traffic summarises API access logs for the analytics page.
package traffic

import (
	"cmp"
	"slices"
	"strings"
)

// TopEndpoints counts hits per endpoint in one pass over the log, then sorts
// only the distinct endpoints by count descending and name ascending and
// returns the first k.
func TopEndpoints(hits []string, k int) []string {
	if k <= 0 {
		return nil
	}
	counts := make(map[string]int)
	for _, h := range hits {
		counts[h]++
	}
	type entry struct {
		endpoint string
		count    int
	}
	entries := make([]entry, 0, len(counts))
	for endpoint, count := range counts {
		entries = append(entries, entry{endpoint, count})
	}
	slices.SortFunc(entries, func(a, b entry) int {
		return cmp.Or(cmp.Compare(b.count, a.count), strings.Compare(a.endpoint, b.endpoint))
	})
	k = min(k, len(entries))
	out := make([]string, k)
	for i := range out {
		out[i] = entries[i].endpoint
	}
	return out
}
