// Package contacts validates address lists for the contact importer.
package contacts

import "strings"

// FirstDuplicate normalizes each address and remembers every normalized form
// seen so far. The first address whose form is already in the set is the
// answer, so a single pass with a hash set is enough.
func FirstDuplicate(emails []string) (string, bool) {
	seen := make(map[string]struct{}, len(emails))
	for _, email := range emails {
		key := strings.ToLower(strings.TrimSpace(email))
		if _, dup := seen[key]; dup {
			return key, true
		}
		seen[key] = struct{}{}
	}
	return "", false
}
