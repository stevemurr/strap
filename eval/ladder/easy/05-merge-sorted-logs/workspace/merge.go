// Package logs combines per-service log streams for the log viewer.
package logs

// Entry is one log line with its timestamp.
type Entry struct {
	At   int64 // Unix milliseconds.
	Line string
}

// Merge interleaves two streams that are each sorted by At into one sorted
// stream, keeping entries from a ahead of entries from b on equal timestamps.
// README.md defines the edge cases.
func Merge(a, b []Entry) []Entry {
	panic("not implemented")
}
