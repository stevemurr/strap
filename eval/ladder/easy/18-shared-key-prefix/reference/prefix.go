// Package config groups settings by the prefix their keys share.
package config

// SharedPrefix starts with the first key as the candidate and shortens it
// against every other key; each comparison stops at the first differing byte
// or at the end of the shorter string, and an empty candidate ends early.
func SharedPrefix(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	prefix := keys[0]
	for _, key := range keys[1:] {
		n := 0
		for n < len(prefix) && n < len(key) && prefix[n] == key[n] {
			n++
		}
		prefix = prefix[:n]
		if prefix == "" {
			break
		}
	}
	return prefix
}
