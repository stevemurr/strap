//go:build !darwin && !linux

package tool

import (
	"os"
	"os/exec"
)

const processGroupsSupported = false

func configureProcessGroup(cmd *exec.Cmd)  {}
func stopProcessGroup(cmd *exec.Cmd) error { return os.ErrProcessDone }
