// Package transport owns a session's provider connection pool and request lifetime.
package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

var ErrClosed = errors.New("session transport closed")

type Transport struct {
	base    *http.Transport
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	closed  bool
	active  int
	drained chan struct{}
}

// New creates an independent pool with standard HTTP transport defaults.
func New() *Transport {
	ctx, cancel := context.WithCancel(context.Background())
	return &Transport{ctx: ctx, cancel: cancel, drained: make(chan struct{}), base: &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		DialContext:       (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2: true, MaxIdleConns: 100, IdleConnTimeout: 90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second,
	}}
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, ErrClosed
	}
	t.active++
	t.mu.Unlock()
	ctx, cancel := context.WithCancelCause(req.Context())
	stop := context.AfterFunc(t.ctx, func() { cancel(ErrClosed) })
	var once sync.Once
	finish := func() {
		once.Do(func() {
			stop()
			cancel(nil)
			t.mu.Lock()
			t.active--
			if t.closed && t.active == 0 {
				close(t.drained)
			}
			t.mu.Unlock()
		})
	}
	response, err := t.base.RoundTrip(req.Clone(ctx))
	if err != nil {
		finish()
		return nil, err
	}
	body := &responseBody{body: response.Body, finish: finish}
	response.Body = body
	stopBody := context.AfterFunc(ctx, func() { _ = body.Close() })
	body.mu.Lock()
	if body.closed {
		stopBody()
	} else {
		body.stop = stopBody
	}
	body.mu.Unlock()
	return response, nil
}

// Close stops admission and cancels active I/O, including unread response bodies.
// A timeout leaves the same shutdown in progress; subsequent Close calls can wait.
func (t *Transport) Close(ctx context.Context) error {
	t.mu.Lock()
	if !t.closed {
		t.closed = true
		t.cancel()
		if t.active == 0 {
			close(t.drained)
		}
	}
	t.mu.Unlock()
	select {
	case <-t.drained:
		t.base.CloseIdleConnections()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type responseBody struct {
	body   io.ReadCloser
	finish func()
	once   sync.Once
	mu     sync.Mutex
	stop   func() bool
	closed bool
	err    error
}

func (b *responseBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if err != nil {
		_ = b.Close()
	}
	return n, err
}
func (b *responseBody) Close() error {
	b.once.Do(func() {
		b.mu.Lock()
		b.closed = true
		stop := b.stop
		b.mu.Unlock()
		if stop != nil {
			stop()
		}
		b.err = b.body.Close()
		b.finish()
	})
	return b.err
}
