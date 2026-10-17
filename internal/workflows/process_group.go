//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package workflows

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureGroupCancellation(cmd *exec.Cmd) {
	// The group ID is the PID returned by this exact Start with Setpgid, never a
	// user-supplied or discovered PID. Children that inherit the group are covered.
	cmd.Cancel = func() error {
		if cmd.Process == nil || cmd.Process.Pid <= 0 {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
}
