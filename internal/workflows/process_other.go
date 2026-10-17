//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package workflows

import (
	"os/exec"
	"time"
)

func pinCommandThread() func() { return func() {} }

// Release targets are Linux. Other targets retain CommandContext's process
// cancellation and bounded pipe cleanup without assuming Unix process groups.
func configureCommand(cmd *exec.Cmd) { cmd.WaitDelay = 2 * time.Second }
