//go:build darwin || linux

package tool

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

const processGroupsSupported = true

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return stopProcessGroup(cmd) }
}

// SIGKILL bounds cancellation for commands that ignore SIGTERM. Processes that
// deliberately leave the group are outside this mechanism; this is no sandbox.
func stopProcessGroup(cmd *exec.Cmd) error {
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
