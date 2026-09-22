package evalweb

import "strings"

// os/exec serializes writes when stdout and stderr share the same writer.
// Bound partial lines and split carriage returns used by build progress displays.
type buildProgressWriter struct {
	pending string
	report  func(JobEvent)
}

func (w *buildProgressWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' || b == '\r' {
			w.flush()
		} else {
			w.pending += string([]byte{b})
			if len(w.pending) >= 4096 {
				w.flush()
			}
		}
	}
	return len(p), nil
}

func (w *buildProgressWriter) flush() {
	if text := strings.TrimSpace(w.pending); text != "" {
		w.report(JobEvent{Kind: "build", Phase: "building", Text: text})
	}
	w.pending = ""
}
