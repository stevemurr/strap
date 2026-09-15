// Package templates lints page templates before they are parsed.
package templates

// Balanced walks the template once, pushing every opener on a stack and
// popping it when the matching closer arrives. Any other byte is skipped, and
// the template is balanced when the stack is empty at the end.
func Balanced(template string) bool {
	var stack []byte
	for i := 0; i < len(template); i++ {
		switch c := template[i]; c {
		case '(', '[', '{':
			stack = append(stack, c)
		case ')', ']', '}':
			if len(stack) == 0 || stack[len(stack)-1] != opener(c) {
				return false
			}
			stack = stack[:len(stack)-1]
		}
	}
	return len(stack) == 0
}

func opener(closer byte) byte {
	switch closer {
	case ')':
		return '('
	case ']':
		return '['
	default:
		return '{'
	}
}
