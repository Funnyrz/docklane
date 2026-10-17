//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package workflows

import (
	"os/exec"
	"syscall"
)

func pinCommandThread() func() { return func() {} }
func configureCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	configureGroupCancellation(cmd)
}
