// Package editor compares keystroke logs by the text they produce.
package editor

// SameTyped walks both logs from the end. Each step finds the next character
// that survives every later backspace in each log and compares the two, so
// neither final text is ever built.
func SameTyped(a, b string) bool {
	i, j := len(a)-1, len(b)-1
	for {
		i = surviving(a, i)
		j = surviving(b, j)
		if i < 0 || j < 0 {
			return i < 0 && j < 0
		}
		if a[i] != b[j] {
			return false
		}
		i--
		j--
	}
}

// surviving returns the index of the last character at or before i that is
// not deleted by a backspace at or before i, or -1 when nothing survives.
func surviving(log string, i int) int {
	pending := 0
	for ; i >= 0; i-- {
		switch {
		case log[i] == '#':
			pending++
		case pending > 0:
			pending--
		default:
			return i
		}
	}
	return -1
}
