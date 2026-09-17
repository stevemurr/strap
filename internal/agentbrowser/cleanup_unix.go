//go:build darwin || linux

package agentbrowser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/stevemurr/strap/internal/webprocess"
)

type processRow struct {
	pid, parent, group int
	command            string
}

// The daemon publishes page.pid inside
// our private socket directory and launches Chrome in a separate process group.
// Killing the CLI cannot cancel an in-flight daemon command, and close waits
// for that command's lock. Force-stop only groups descended from our daemon,
// then the daemon itself. The caller owns/removes the explicit temporary profile.
func interruptBrowser(ctx context.Context, dir, namespace string) (bool, error) {
	return interruptBrowserWith(ctx, dir, namespace, cleanupOps{os.ReadFile, webprocess.Run, syscall.Kill})
}

// Keep process inspection and signaling together so cleanup can be verified
// against an isolated process table without sending signals to real browsers.
type cleanupOps struct {
	readFile func(string) ([]byte, error)
	run      func(context.Context, string, []string, string, int, ...string) ([]byte, error)
	kill     func(int, syscall.Signal) error
}

func interruptBrowserWith(ctx context.Context, dir, namespace string, ops cleanupOps) (bool, error) {
	path := filepath.Join(dir, "namespaces", namespace, "run", "page.pid")
	raw, err := ops.readFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read owned browser daemon PID: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		return false, errors.New("invalid owned browser daemon PID")
	}
	out, err := ops.run(ctx, "/bin/ps", []string{"-axo", "pid=,ppid=,pgid=,comm="}, "", 4<<20)
	if err != nil {
		return false, fmt.Errorf("inspect owned browser processes: %w", err)
	}
	var rows []processRow
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		p, e1 := strconv.Atoi(fields[0])
		parent, e2 := strconv.Atoi(fields[1])
		group, e3 := strconv.Atoi(fields[2])
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		rows = append(rows, processRow{p, parent, group, strings.Join(fields[3:], " ")})
	}
	groups, err := ownedBrowserGroups(pid, rows)
	if err != nil {
		return false, err
	}
	found := false
	for _, row := range rows {
		found = found || row.pid == pid
	}
	if !found {
		return false, nil
	}
	for _, group := range groups {
		// Match ChromeProcess::kill in the backend: stop the leader
		// first, then its helpers. macOS can refuse group-wide delivery even
		// when stopping the owned browser leader is permitted.
		if err := ops.kill(group, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return false, fmt.Errorf("stop owned browser process %d: %w", group, err)
		}
		if err := ops.kill(-group, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.EPERM) {
			return false, fmt.Errorf("stop owned browser helpers %d: %w", group, err)
		}
	}
	if err := ops.kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return false, fmt.Errorf("stop owned browser daemon: %w", err)
	}
	return true, nil
}

func ownedBrowserGroups(daemon int, rows []processRow) ([]int, error) {
	found := false
	for _, row := range rows {
		if row.pid != daemon {
			continue
		}
		if row.group != daemon || !strings.HasPrefix(filepath.Base(row.command), "agent-browser") {
			return nil, errors.New("owned daemon PID no longer identifies agent-browser; refusing process cleanup")
		}
		found = true
	}
	if !found {
		return nil, nil
	}
	descendants := map[int]bool{daemon: true}
	for {
		changed := false
		for _, row := range rows {
			if descendants[row.parent] && !descendants[row.pid] {
				descendants[row.pid] = true
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	var groups []int
	for _, row := range rows {
		if row.pid != daemon && row.pid > 1 && row.group == row.pid && descendants[row.pid] {
			groups = append(groups, row.group)
		}
	}
	return groups, nil
}
