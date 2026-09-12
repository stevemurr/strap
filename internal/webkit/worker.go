// Package webkit owns a lazy, multiplexed wkrender protocol-1 worker.
package webkit

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/stevemurr/strap/internal/webprocess"
)

type Page struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	HTML  string `json:"html"`
}

type result struct {
	page Page
	err  error
}
type process struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	write   chan struct{}
	ready   chan error
	done    chan struct{}
	pending map[string]chan result // protected by Worker.mu
	err     error
}

type Worker struct {
	program   string
	mu        sync.Mutex
	current   *process
	closed    bool
	sequence  uint64
	startGate chan struct{}
	slots     chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	command   func(string, ...string) *exec.Cmd
}

func New(program string) *Worker {
	ctx, cancel := context.WithCancel(context.Background())
	return &Worker{program: program, startGate: make(chan struct{}, 1), slots: make(chan struct{}, 4), ctx: ctx, cancel: cancel}
}

func (w *Worker) start(ctx context.Context) (*process, error) {
	select {
	case w.startGate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-w.ctx.Done():
		return nil, errors.New("webkit worker closed")
	}
	defer func() { <-w.startGate }()
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil, errors.New("webkit worker closed")
	}
	if w.current != nil {
		p := w.current
		w.mu.Unlock()
		return p, nil
	}
	command := w.command
	if command == nil {
		command = exec.Command
	}
	cmd := command(w.program, "--worker", "--max-concurrency", "4")
	cmd.Env = webprocess.Env()
	cmd.WaitDelay = 250 * time.Millisecond
	webprocess.Configure(cmd)
	// Configure supplies cancellation only for CommandContext users.
	cmd.Cancel = nil
	stderr := &webprocess.Buffer{Limit: 8192}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		w.mu.Unlock()
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		stdout.Close()
		w.mu.Unlock()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		stdout.Close()
		stdin.Close()
		w.mu.Unlock()
		return nil, fmt.Errorf("start wkrender: %w", err)
	}
	p := &process{cmd: cmd, stdin: stdin, stdout: stdout, write: make(chan struct{}, 1), ready: make(chan error, 1), done: make(chan struct{}), pending: make(map[string]chan result)}
	w.current = p
	w.wg.Add(1)
	w.mu.Unlock()
	go w.read(p, stdout, stderr)
	startup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case err := <-p.ready:
		if err != nil {
			w.abort(p, err)
		}
		return p, err
	case <-startup.Done():
		w.abort(p, fmt.Errorf("wkrender handshake: %w", startup.Err()))
		return nil, startup.Err()
	case <-w.ctx.Done():
		return nil, errors.New("webkit worker closed")
	}
}

func (w *Worker) read(p *process, out io.Reader, stderr *webprocess.Buffer) {
	defer w.wg.Done()
	defer close(p.done)
	var failure error
	defer func() {
		if failure == nil {
			failure = errors.New("wkrender closed its output")
		}
		w.abort(p, failure)
		_ = p.cmd.Wait() // exactly one waiter; abort closes pipes/process group
	}()
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 64<<10), (32<<20)+1)
	if !scanner.Scan() {
		diagnostic, _ := stderr.Snapshot()
		failure = fmt.Errorf("wkrender did not send handshake: %s", diagnostic)
		p.ready <- failure
		return
	}
	var ready struct {
		Type           string `json:"type"`
		Protocol       int    `json:"protocol"`
		MaxConcurrency int    `json:"max_concurrency"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &ready); err != nil || ready.Type != "ready" || ready.Protocol != 1 || ready.MaxConcurrency < 4 {
		failure = errors.New("wkrender requires worker protocol 1 with four slots; update wkrender")
		p.ready <- failure
		return
	}
	p.ready <- nil
	for scanner.Scan() {
		var row struct {
			ID    string `json:"id"`
			OK    *bool  `json:"ok"`
			Page  *Page  `json:"page"`
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil || row.ID == "" || row.OK == nil || (*row.OK && row.Page == nil) {
			failure = errors.New("invalid wkrender response")
			return
		}
		r := result{}
		if *row.OK {
			r.page = *row.Page
		} else {
			r.err = fmt.Errorf("wkrender %s: %s", row.Code, row.Error)
		}
		w.mu.Lock()
		ch := p.pending[row.ID]
		delete(p.pending, row.ID)
		if ch != nil {
			ch <- r
		}
		w.mu.Unlock()
	}
	if err := scanner.Err(); err != nil {
		failure = fmt.Errorf("read wkrender response: %w", err)
	}
}

func (w *Worker) abort(p *process, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if p.err != nil {
		return
	}
	p.err = err
	if w.current == p {
		w.current = nil
	}
	for id, ch := range p.pending {
		ch <- result{err: err}
		delete(p.pending, id)
	}
	_ = webprocess.Kill(p.cmd)
	_ = p.stdin.Close()
	_ = p.stdout.Close()
}

func (w *Worker) send(ctx context.Context, p *process, packet any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(packet)
	if err != nil {
		return err
	}
	if len(data) >= 1<<20 {
		return errors.New("wkrender request exceeds 1 MiB")
	}
	select {
	case p.write <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return errors.New("wkrender stopped")
	}
	defer func() { <-p.write }()
	w.mu.Lock()
	failure := p.err
	w.mu.Unlock()
	if failure != nil {
		return failure
	}
	written := make(chan error, 1)
	go func() { _, err := p.stdin.Write(append(data, '\n')); written <- err }()
	select {
	case err := <-written:
		if err != nil {
			w.abort(p, fmt.Errorf("write wkrender: %w", err))
		}
		return err
	case <-ctx.Done():
		w.abort(p, fmt.Errorf("write wkrender: %w", ctx.Err()))
		<-written
		return ctx.Err()
	}
}

func (w *Worker) Search(ctx context.Context, url string) (Page, error) {
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	select {
	case w.slots <- struct{}{}:
	case <-ctx.Done():
		return Page{}, ctx.Err()
	case <-w.ctx.Done():
		return Page{}, errors.New("webkit worker closed")
	}
	defer func() { <-w.slots }()
	p, err := w.start(ctx)
	if err != nil {
		return Page{}, err
	}
	w.mu.Lock()
	if p.err != nil {
		err := p.err
		w.mu.Unlock()
		return Page{}, err
	}
	w.sequence++
	id := strconv.FormatUint(w.sequence, 10)
	ch := make(chan result, 1)
	p.pending[id] = ch
	w.mu.Unlock()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(20 * time.Second)
	}
	packet := map[string]any{"id": id, "method": "render", "url": url, "timeout": max(0.001, time.Until(deadline).Seconds()), "readiness": "search"}
	if err := w.send(ctx, p, packet); err != nil {
		w.mu.Lock()
		delete(p.pending, id)
		w.mu.Unlock()
		return Page{}, err
	}
	select {
	case r := <-ch:
		return r.page, r.err
	case <-ctx.Done():
		// Keep the slot until the worker acknowledges cancellation. Otherwise a
		// new request can race a still-running view and be rejected as busy.
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := w.send(cleanup, p, map[string]string{"id": id, "method": "cancel"}); err != nil {
			w.abort(p, err)
		}
		select {
		case <-ch:
		case <-cleanup.Done():
			w.abort(p, errors.New("wkrender did not acknowledge cancellation"))
		}
		return Page{}, ctx.Err()
	}
}

func (w *Worker) Close(ctx context.Context) error {
	w.mu.Lock()
	w.closed = true
	w.cancel()
	p := w.current
	w.mu.Unlock()
	if p != nil {
		w.abort(p, errors.New("webkit worker closed"))
	}
	done := make(chan struct{})
	go func() { w.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
