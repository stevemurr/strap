package tool

// valueOrZero is used only by explicit domain adapters where null means no
// supplied value. Parameter decoding itself never collapses null into zero.
func valueOrZero[T any](p *T) T {
	if p != nil {
		return *p
	}
	var zero T
	return zero
}

func mapInputs[A, B any](values []A, convert func(A) B) []B {
	if values == nil {
		return nil
	}
	out := make([]B, len(values))
	for i, v := range values {
		out[i] = convert(v)
	}
	return out
}

func mapPointer[A, B any](p *A, convert func(A) B) *B {
	if p == nil {
		return nil
	}
	v := convert(*p)
	return &v
}
