// Package pricing computes bundle prices from per-item multiplicative factors.
package pricing

// ExclusiveProducts fills out[i] with the product of factors[:i] on a forward
// pass, then multiplies in the product of factors[i+1:] on a backward pass.
// Zeros need no special handling because nothing is divided.
func ExclusiveProducts(factors []int) []int {
	if len(factors) < 2 {
		return nil
	}
	out := make([]int, len(factors))
	prefix := 1
	for i, f := range factors {
		out[i] = prefix
		prefix *= f
	}
	suffix := 1
	for i := len(factors) - 1; i >= 0; i-- {
		out[i] *= suffix
		suffix *= factors[i]
	}
	return out
}
