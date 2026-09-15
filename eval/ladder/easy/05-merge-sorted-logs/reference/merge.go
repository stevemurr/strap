// Package logs combines per-service log streams for the log viewer.
package logs

// Entry is one log line with its timestamp.
type Entry struct {
	At   int64 // Unix milliseconds.
	Line string
}

// Merge walks both inputs with a cursor each and appends the smaller head to
// the output. Only a strictly smaller b entry goes first, so a wins ties and
// each input keeps its own order.
func Merge(a, b []Entry) []Entry {
	out := make([]Entry, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if b[j].At < a[i].At {
			out = append(out, b[j])
			j++
		} else {
			out = append(out, a[i])
			i++
		}
	}
	out = append(out, a[i:]...)
	out = append(out, b[j:]...)
	return out
}
