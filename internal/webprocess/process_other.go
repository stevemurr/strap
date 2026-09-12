//go:build !darwin && !linux

package webprocess

import "os/exec"

func Configure(cmd *exec.Cmd) {}
func Kill(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
