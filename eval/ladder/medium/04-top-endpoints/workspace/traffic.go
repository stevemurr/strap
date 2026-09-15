// Package traffic summarises API access logs for the analytics page.
package traffic

// TopEndpoints returns the k most frequently hit endpoints, most frequent
// first, with ties broken by ascending name. README.md defines the edge cases
// and the performance requirement.
func TopEndpoints(hits []string, k int) []string {
	panic("not implemented")
}
