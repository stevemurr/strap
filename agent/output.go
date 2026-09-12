package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
	"hash"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const outputChunkBytes = 16 << 10

// outputBuffer owns at most one chunk of pending text. The timer flushes even
// when the provider has stopped invoking callbacks. Publication uses drain life.
type outputBuffer struct {
	mu                sync.Mutex
	agent             *Agent
	id                identity.OutputID
	execution         context.Context
	cancel            context.CancelFunc
	pending           string
	accepted          uint64
	observed          hash.Hash
	observedBytes     uint64
	publicationFailed bool
	err               error
	stop, done        chan struct{}
}

func newOutputBuffer(a *Agent, ctx context.Context, cancel context.CancelFunc, id identity.OutputID) *outputBuffer {
	b := &outputBuffer{observed: sha256.New(), agent: a, id: id, execution: ctx, cancel: cancel, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(b.done)
		ticker := time.NewTicker(40 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				b.mu.Lock()
				b.flush()
				b.mu.Unlock()
			case <-b.stop:
				return
			}
		}
	}()
	return b
}
func (b *outputBuffer) flush() {
	if b.err != nil || b.pending == "" {
		return
	}
	if err := b.agent.report(OutputDelta{Output: b.id, Offset: b.accepted, Text: b.pending}); err != nil {
		b.err = err
		b.publicationFailed = true
		b.cancel()
		return
	}
	b.accepted += uint64(len(b.pending))
	b.pending = ""
}
func (b *outputBuffer) OnDelta(d provider.Delta) error {
	if err := b.execution.Err(); err != nil {
		return err
	}
	if !utf8.ValidString(d.Text) {
		return errors.New("provider delta is not valid UTF-8")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	b.observed.Write([]byte(d.Text))
	b.observedBytes += uint64(len(d.Text))
	text := d.Text
	for len(text) > 0 {
		n := min(len(text), outputChunkBytes-len(b.pending))
		for n > 0 && n < len(text) && !utf8.RuneStart(text[n]) {
			n--
		}
		if n == 0 {
			b.flush()
			if b.err != nil {
				return b.err
			}
			continue
		}
		b.pending += text[:n]
		text = text[n:]
		if len(b.pending) >= outputChunkBytes-utf8.UTFMax {
			b.flush()
			if b.err != nil {
				return b.err
			}
		}
	}
	return nil
}
func (b *outputBuffer) finish(final string, success bool) (uint64, error) {
	if success {
		b.mu.Lock()
		prefix := b.observed.Sum(nil)
		observedBytes := b.observedBytes
		b.mu.Unlock()
		if !utf8.ValidString(final) || uint64(len(final)) < observedBytes || !prefixMatches(final, observedBytes, prefix) {
			b.mu.Lock()
			b.err = errors.New("final response conflicts with streamed text")
			b.mu.Unlock()
		} else if suffix := final[observedBytes:]; suffix != "" {
			if err := b.OnDelta(provider.Delta{Text: suffix}); err != nil {
				b.mu.Lock()
				b.err = errors.Join(b.err, err)
				b.mu.Unlock()
			}
		}
	}
	close(b.stop)
	<-b.done
	b.mu.Lock()
	defer b.mu.Unlock()
	// A provider failure must not discard accepted callbacks still pending flush.
	prior := b.err
	b.err = nil
	if !b.publicationFailed {
		b.flush()
	}
	b.err = errors.Join(prior, b.err)
	return b.accepted, b.err
}
func (a *Agent) generate(ctx context.Context, request provider.Request, revision uint64) (provider.Response, identity.OutputID, error) {
	a.nextOutput++
	id := identity.OutputID{Agent: a.config.ID, Call: a.nextOutput}
	if err := a.report(OutputStarted{Output: id, ContextRevision: revision, StartedAt: time.Now().UTC()}); err != nil {
		return provider.Response{}, id, err
	}
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	b := newOutputBuffer(a, run, cancel, id)
	response, err := a.config.Spec.Provider.Submit(run, request, b)
	bytes, flushErr := b.finish(response.Content, err == nil)
	err = errors.Join(err, flushErr, a.recordUsage(revision, response.Usage), a.reportError())
	var position *uint64
	status := OutputFailed
	if err == nil {
		a.control.mu.Lock()
		if ctx.Err() != nil || a.stopRequested.Load() {
			err = context.Canceled
		} else {
			response.ToolCalls = provider.CopyCalls(response.ToolCalls)
			if strings.TrimSpace(response.Content) == "" && len(response.ToolCalls) == 0 {
				err = errors.New("model returned no text or tool calls")
			} else {
				pos := a.thread.append(provider.Message{Role: "assistant", Content: content.Text(response.Content), ToolCalls: response.ToolCalls})
				position = &pos
			}
		}
		a.control.mu.Unlock()
		if position != nil {
			err = a.report(HistoryAppended{Position: *position, Message: provider.Message{Role: "assistant", Content: content.Text(response.Content), ToolCalls: provider.CopyCalls(response.ToolCalls)}, Output: &id})
			if err != nil {
				return response, id, err
			} // Keep committed history; no invented terminal.
			status = OutputComplete
		}
	}
	if err != nil && errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		status = OutputCanceled
	}
	finishErr := a.report(OutputFinished{Output: id, Status: status, Bytes: bytes, HistoryPosition: position, Err: err, FinishedAt: time.Now().UTC()})
	return response, id, errors.Join(err, finishErr)
}

func prefixMatches(text string, n uint64, want []byte) bool {
	sum := sha256.Sum256([]byte(text[:n]))
	return bytes.Equal(sum[:], want)
}
