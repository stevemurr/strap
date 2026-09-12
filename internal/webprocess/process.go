// Package webprocess provides bounded subprocess I/O for the web backends.
package webprocess

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Buffer drains all writes but retains at most Limit bytes. It is safe for
// concurrent readers, including when a worker is still writing diagnostics.
type Buffer struct {
	mu       sync.Mutex
	Limit    int
	data     []byte
	overflow bool
}

func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := min(len(p), max(0, b.Limit-len(b.data)))
	b.data = append(b.data, p[:n]...)
	b.overflow = b.overflow || n < len(p)
	return len(p), nil
}

func (b *Buffer) Snapshot() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.data), b.overflow
}

// Env excludes ambient browser configuration and credentials. The explicit
// agent-browser config and session are supplied separately by the host.
func Env() []string {
	var env []string
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "SYSTEMROOT"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func Run(ctx context.Context, program string, args []string, dir string, limit int, extraEnv ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir, cmd.Env = dir, append(Env(), extraEnv...)
	cmd.WaitDelay = 250 * time.Millisecond
	Configure(cmd)
	stdout, stderr := &Buffer{Limit: limit}, &Buffer{Limit: 8192}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", program, err)
	}
	defer Kill(cmd)
	err := cmd.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	out, overflow := stdout.Snapshot()
	if overflow {
		return nil, fmt.Errorf("backend response exceeded %d bytes", limit)
	}
	if err != nil {
		diagnostic, _ := stderr.Snapshot()
		// JSON failures often arrive on stdout. Preserve bounded diagnostics.
		if len(diagnostic) == 0 {
			diagnostic = out[:min(len(out), 8192)]
		}
		return nil, fmt.Errorf("backend command failed: %w: %s", err, diagnostic)
	}
	return out, nil
}
