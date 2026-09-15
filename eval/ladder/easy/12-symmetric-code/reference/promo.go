// Package promo validates candidate promo codes for the code generator.
package promo

// IsSymmetric walks the code from both ends at once, skipping bytes that are
// not ASCII letters or digits and comparing the rest without regard to case.
func IsSymmetric(code string) bool {
	i, j := 0, len(code)-1
	for i < j {
		for i < j && !counts(code[i]) {
			i++
		}
		for i < j && !counts(code[j]) {
			j--
		}
		if fold(code[i]) != fold(code[j]) {
			return false
		}
		i++
		j--
	}
	return true
}

func counts(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func fold(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + 'a' - 'A'
	}
	return b
}
