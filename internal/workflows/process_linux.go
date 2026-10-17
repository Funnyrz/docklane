//go:build linux

package workflows

import (
	"os/exec"
	"runtime"
	"syscall"
)

// Linux binds the death signal to the thread that forks the child. Keep that
// thread alive and attached until Wait completes, rather than relying on the
// lifetime of an arbitrary Go runtime thread.
func pinCommandThread() func() { runtime.LockOSThread(); return runtime.UnlockOSThread }
func configureCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	configureGroupCancellation(cmd)
}
