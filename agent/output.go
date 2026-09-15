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

type channelProgress struct {
	accepted      uint64
	observed      hash.Hash
	observedBytes uint64
}

// outputBuffer owns at most one chunk of pending text. The timer flushes even
// when the provider has stopped invoking callbacks. Publication uses drain life.
type outputBuffer struct {
	mu                 sync.Mutex
	agent              *Agent
	id                 identity.OutputID
	execution          context.Context
	cancel             context.CancelFunc
	pending            string
	channel            provider.OutputChannel
	content, reasoning channelProgress
	limit              uint64 // Reasoning bytes allowed; zero is unlimited.
	publicationFailed  bool
	err                error
	stop, done         chan struct{}
}

func newOutputBuffer(a *Agent, ctx context.Context, cancel context.CancelFunc, id identity.OutputID) *outputBuffer {
	b := &outputBuffer{agent: a, id: id, execution: ctx, cancel: cancel, stop: make(chan struct{}), done: make(chan struct{}), content: channelProgress{observed: sha256.New()}, reasoning: channelProgress{observed: sha256.New()}}
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
func (b *outputBuffer) progress(c provider.OutputChannel) *channelProgress {
	if c == provider.ChannelReasoning {
		return &b.reasoning
	}
	return &b.content
}
func (b *outputBuffer) flush() {
	if b.err != nil || b.pending == "" {
		return
	}
	progress := b.progress(b.channel)
	if err := b.agent.report(OutputDelta{Output: b.id, Channel: b.channel, Offset: progress.accepted, Text: b.pending}); err != nil {
		b.err = err
		b.publicationFailed = true
		b.cancel()
		return
	}
	progress.accepted += uint64(len(b.pending))
	b.pending = ""
}
func (b *outputBuffer) OnDelta(d provider.Delta) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	reject := func(err error) error { b.err = err; b.cancel(); return err }
	if err := b.execution.Err(); err != nil {
		return reject(err)
	}
	d.Channel = provider.NormalizeChannel(d.Channel)
	if !d.Channel.Valid() {
		return reject(errors.New("invalid output channel"))
	}
	if !utf8.ValidString(d.Text) {
		return reject(errors.New("provider delta is not valid UTF-8"))
	}
	if d.Text == "" {
		return nil
	}
	if b.channel != d.Channel {
		b.flush()
		if b.err != nil {
			return b.err
		}
		b.channel = d.Channel
	}
	progress := b.progress(d.Channel)
	progress.observed.Write([]byte(d.Text))
	progress.observedBytes += uint64(len(d.Text))
	if d.Channel == provider.ChannelReasoning && b.limit > 0 && progress.observedBytes > b.limit {
		return reject(ErrReasoningLimit)
	}
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
func (b *outputBuffer) finish(final provider.Response, success bool) (uint64, uint64, error) {
	if success {
		// Validate both prefixes before publishing either missing suffix.
		b.mu.Lock()
		channels := []struct {
			channel  provider.OutputChannel
			text     string
			progress *channelProgress
		}{
			{provider.ChannelReasoning, final.Reasoning, &b.reasoning},
			{provider.ChannelContent, final.Content, &b.content},
		}
		valid := b.err == nil
		for _, v := range channels {
			if !utf8.ValidString(v.text) || uint64(len(v.text)) < v.progress.observedBytes || !prefixMatches(v.text, v.progress.observedBytes, v.progress.observed.Sum(nil)) {
				b.err = errors.Join(b.err, errors.New("final response conflicts with streamed "+string(v.channel)))
				valid = false
			}
		}
		b.mu.Unlock()
		if valid {
			for _, v := range channels {
				if suffix := v.text[v.progress.observedBytes:]; suffix != "" {
					if err := b.OnDelta(provider.Delta{Channel: v.channel, Text: suffix}); err != nil {
						break
					}
				}
			}
		}
	}
	close(b.stop)
	<-b.done
	b.mu.Lock()
	defer b.mu.Unlock()
	// Accepted callbacks remain recoverable even when completion validation fails.
	prior := b.err
	b.err = nil
	if !b.publicationFailed {
		b.flush()
	}
	b.err = errors.Join(prior, b.err)
	return b.content.accepted, b.reasoning.accepted, b.err
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
	b.limit = a.config.Spec.ReasoningLimit
	response, err := a.config.Spec.Provider.Submit(run, request, b)
	bytes, reasoningBytes, flushErr := b.finish(response, err == nil)
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
	if err != nil && errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, ErrReasoningLimit) {
		status = OutputCanceled
	}
	finishErr := a.report(OutputFinished{Output: id, Status: status, Bytes: bytes, ReasoningBytes: reasoningBytes, HistoryPosition: position, Err: err, FinishedAt: time.Now().UTC()})
	return response, id, errors.Join(err, finishErr)
}

func prefixMatches(text string, n uint64, want []byte) bool {
	sum := sha256.Sum256([]byte(text[:n]))
	return bytes.Equal(sum[:], want)
}
