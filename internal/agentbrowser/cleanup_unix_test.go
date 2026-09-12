//go:build darwin || linux

package agentbrowser

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func TestCleanupOnlySelectsOwnedDescendantGroups(t *testing.T) {
	rows := []processRow{
		{100, 1, 100, "/path/agent-browser"},
		{200, 100, 200, "/Applications/Google Chrome"},
		{201, 200, 200, "renderer"},
		{202, 200, 202, "crash handler"},
		{300, 1, 300, "/Applications/Google Chrome"}, // user's browser
		{301, 300, 300, "renderer"},
		{400, 1, 400, "/path/agent-browser"}, // another operation
		{500, 400, 500, "Chrome"},
	}
	groups, err := ownedBrowserGroups(100, rows)
	if err != nil || !slices.Equal(groups, []int{200, 202}) {
		t.Fatalf("selected unrelated process groups: %v %v", groups, err)
	}
	rows[0].command = "/unrelated/program"
	if _, err := ownedBrowserGroups(100, rows); err == nil {
		t.Fatal("accepted reused daemon PID")
	}
	if groups, err := ownedBrowserGroups(999, rows); err != nil || len(groups) != 0 {
		t.Fatal("missing daemon has descendants")
	}
}

func TestInterruptBrowserInspectsAndSignalsOnlyOwnedProcesses(t *testing.T) {
	const table = "100 1 100 /path/agent-browser\n202 201 202 crash handler\n201 200 200 renderer\n200 100 200 Chrome\n300 1 300 User Chrome\n\nx 1 1 bad\n1 x 1 bad\n1 1 x bad\nshort\n"
	for _, tc := range []struct {
		name, pid, table, want string
		readErr, runErr        error
		failPID                int
		killErr                error
		killed                 bool
		signals                []int
	}{
		{name: "missing", readErr: os.ErrNotExist},
		{name: "unreadable", readErr: os.ErrPermission, want: "read owned"},
		{name: "malformed PID", pid: "nope", want: "invalid owned"},
		{name: "init PID", pid: "1", want: "invalid owned"},
		{name: "inspection failure", pid: "100", runErr: errors.New("ps failed"), want: "inspect owned"},
		{name: "reused PID", pid: "100", table: "100 1 100 unrelated", want: "refusing"},
		{name: "departed daemon", pid: "100", table: "300 1 300 Chrome"},
		{name: "owned groups", pid: "100\n", table: table, killed: true, signals: []int{202, -202, 200, -200, 100}},
		{name: "leader failure", pid: "100", table: table, failPID: 202, killErr: syscall.EPERM, want: "stop owned browser process", signals: []int{202}},
		{name: "helper failure", pid: "100", table: table, failPID: -202, killErr: syscall.EINVAL, want: "stop owned browser helpers", signals: []int{202, -202}},
		{name: "daemon failure", pid: "100", table: table, failPID: 100, killErr: syscall.EPERM, want: "stop owned browser daemon", signals: []int{202, -202, 200, -200, 100}},
		{name: "leader departed", pid: "100", table: table, failPID: 202, killErr: syscall.ESRCH, killed: true, signals: []int{202, -202, 200, -200, 100}},
		{name: "helpers departed", pid: "100", table: table, failPID: -202, killErr: syscall.ESRCH, killed: true, signals: []int{202, -202, 200, -200, 100}},
		{name: "helper permission", pid: "100", table: table, failPID: -202, killErr: syscall.EPERM, killed: true, signals: []int{202, -202, 200, -200, 100}},
		{name: "daemon departed", pid: "100", table: table, failPID: 100, killErr: syscall.ESRCH, killed: true, signals: []int{202, -202, 200, -200, 100}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var signals []int
			ops := cleanupOps{
				readFile: func(path string) ([]byte, error) {
					if path != filepath.Join("private", "namespaces", "owned", "run", "page.pid") {
						t.Error(path)
					}
					return []byte(tc.pid), tc.readErr
				},
				run: func(_ context.Context, program string, args []string, _ string, limit int, _ ...string) ([]byte, error) {
					if program != "/bin/ps" || !slices.Equal(args, []string{"-axo", "pid=,ppid=,pgid=,comm="}) || limit != 4<<20 {
						t.Error(program, args, limit)
					}
					return []byte(tc.table), tc.runErr
				},
				kill: func(pid int, signal syscall.Signal) error {
					signals = append(signals, pid)
					if signal != syscall.SIGKILL {
						t.Error(signal)
					}
					if pid == tc.failPID {
						return tc.killErr
					}
					return nil
				},
			}
			killed, err := interruptBrowserWith(context.Background(), "private", "owned", ops)
			if killed != tc.killed || (tc.want == "" && err != nil) || (tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want))) {
				t.Fatal(killed, err)
			}
			if !slices.Equal(signals, tc.signals) {
				t.Fatalf("signals %v, want %v", signals, tc.signals)
			}
		})
	}
}
