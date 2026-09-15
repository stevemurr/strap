// Package inventory reconciles the SKU lists exported by the warehouses.
package inventory

import "sort"

// CommonStock counts the units of each SKU in a, then walks b taking one
// count off per matching unit, so every SKU is kept min(count in a, count in b)
// times. The matches are sorted before being returned.
func CommonStock(a, b []string) []string {
	counts := make(map[string]int, len(a))
	for _, sku := range a {
		counts[sku]++
	}
	var out []string
	for _, sku := range b {
		if counts[sku] > 0 {
			counts[sku]--
			out = append(out, sku)
		}
	}
	sort.Strings(out)
	return out
}
