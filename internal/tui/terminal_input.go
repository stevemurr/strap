package tui

import (
	"bytes"
	"io"
	"reflect"
	"sync"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// Bubble Tea v1 treats short reads as event boundaries. Keep partial terminal
// sequences and UTF-8 characters out of those reads. The source must not be
// read ahead: Bubble Tea's cancelreader owns readiness and terminal release.
type terminalInputReader struct {
	source      io.Reader
	escapeReady func() (bool, error)
	pending     []byte
	paste       bool
	discardCSI  bool
	err         error
	keyMu       sync.Mutex
	keys        []*tea.KeyMsg
}

// Bubble Tea's unknown CSI messages alias its reusable read buffer. Decode
// enhanced keys before returning the bytes, replacing them with NUL markers.
// NUL produces a value-only key event; this FIFO restores the decoded event in
// order without ever reading an aliased unknown-CSI message in the UI loop.
func (r *terminalInputReader) queueKey(key *tea.KeyMsg) {
	r.keyMu.Lock()
	r.keys = append(r.keys, key)
	r.keyMu.Unlock()
}

func (r *terminalInputReader) filter(_ tea.Model, msg tea.Msg) tea.Msg {
	if key, ok := msg.(tea.KeyMsg); ok && key.Type == tea.KeyCtrlAt && !key.Alt {
		r.keyMu.Lock()
		defer r.keyMu.Unlock()
		if len(r.keys) > 0 {
			key := r.keys[0]
			r.keys[0] = nil
			r.keys = r.keys[1:]
			if key == nil {
				return nil
			}
			return *key
		}
	}
	t := reflect.TypeOf(msg)
	if t != nil && t.PkgPath() == "github.com/charmbracelet/bubbletea" && t.Name() == "unknownCSISequenceMsg" {
		return nil
	}
	return msg
}

func (r *terminalInputReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.err != nil {
		return 0, r.err
	}
	// Bubble Tea supplies a 256-byte buffer. Reserve one byte so a trailing
	// Escape can be joined to a continuation without reading ahead.
	if len(p) < 8 {
		return 0, io.ErrShortBuffer
	}
	// Bound malformed/oversized CSI reports. Never release their parameters as
	// typing. Real mouse and enhanced-keyboard reports fit below this size.
	if len(r.pending) >= len(p)-1 {
		r.discardCSI = bytes.HasPrefix(r.pending, []byte("\x1b["))
		r.pending = nil
	}
	n := copy(p, r.pending)
	r.pending = nil
	count, err := r.source.Read(p[n : len(p)-1])
	n += count
	r.err = err
	end := r.boundary(p[:n])
	// Only a lone Escape has a timeout. CSI fragments wait for their final
	// byte across reads, without blocking Bubble Tea's cancellation mechanism.
	for !r.paste && end == n-1 && p[end] == 0x1b {
		if r.err != nil {
			end = n
			break
		}
		if r.escapeReady == nil {
			break
		}
		ready, waitErr := r.escapeReady()
		if waitErr != nil {
			r.err = waitErr
			break
		}
		if !ready {
			end = n
			break
		}
		if n == len(p) {
			// The descriptor is ready, so the outer cancelreader will call us
			// again to join this Escape with its continuation.
			break
		}
		count, err = r.source.Read(p[n:])
		n += count
		r.err = err
		end += r.boundary(p[end:n])
	}
	r.pending = append(r.pending, p[end:n]...)
	if end > 0 {
		return end, nil
	}
	if r.err != nil {
		return 0, r.err
	}
	return 0, nil
}

func (r *terminalInputReader) boundary(p []byte) int {
	for i := 0; i < len(p); {
		if r.discardCSI {
			if p[i] < 0x20 {
				// A new Escape or a shortcut abandons the broken report.
				r.discardCSI = false
				continue
			}
			// Replace discarded bytes with NUL (ignored by the composer), retaining
			// byte positions so the caller can return one contiguous complete prefix.
			b := p[i]
			p[i] = 0
			r.queueKey(nil)
			i++
			if b >= 0x40 && b <= 0x7e {
				r.discardCSI = false
			}
			continue
		}
		if r.paste {
			end := []byte("\x1b[201~")
			if j := bytes.Index(p[i:], end); j >= 0 {
				i += j + len(end)
				r.paste = false
				continue
			}
			// Let Bubble Tea accumulate the paste, but keep a split closing marker.
			for k := min(len(end)-1, len(p)-i); k > 0; k-- {
				if bytes.Equal(p[len(p)-k:], end[:k]) {
					return len(p) - k
				}
			}
			return len(p)
		}
		start := i
		if p[i] == 0 {
			r.queueKey(&tea.KeyMsg{Type: tea.KeyCtrlAt})
		}
		if p[i] == 0x1b {
			i++
			if i == len(p) {
				return start
			}
			switch p[i] {
			case '[', 'O':
				i++
				if i == len(p) {
					return start
				}
				// Legacy X10 mouse encoding has three arbitrary coordinate bytes.
				if p[start+1] == '[' && p[i] == 'M' {
					if len(p)-i < 4 {
						return start
					}
					i += 4
					continue
				}
				for i < len(p) && p[i] >= 0x20 && p[i] <= 0x3f {
					i++
				}
				if i == len(p) {
					return start
				}
				if p[i] < 0x40 || p[i] > 0x7e {
					// An interrupted report must not turn its parameters into
					// typing or swallow the interrupting key/new sequence.
					clear(p[start:i])
					for range i - start {
						r.queueKey(nil)
					}
					continue
				}
				i++
				if bytes.Equal(p[start:i], []byte("\x1b[200~")) {
					r.paste = true
				} else if key, ok := decodeTerminalKey(string(p[start:i])); ok {
					clear(p[start:i])
					r.queueKey(&key)
					for range i - start - 1 {
						r.queueKey(nil)
					}
				}
				continue
			}
		}
		if !utf8.FullRune(p[i:]) {
			return start
		}
		_, size := utf8.DecodeRune(p[i:])
		i += size
	}
	return len(p)
}
