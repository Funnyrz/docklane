//go:build linux

package workflows

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// RunWorker is the image's internal backup-worker entry point. The outer worker
// starts as root only to set the selected repository owner's identity. Restic
// receives that UID/GID and solely ambient DAC_READ_SEARCH, so repository files
// remain accessible to the manager while read-only sources retain their owners.
func RunWorker(args []string) error {
	if e := validateWorkerArgs(args); e != nil {
		return e
	}
	uid, e := strconv.ParseUint(os.Getenv("DM_WORKER_UID"), 10, 32)
	if e != nil {
		return errors.New("invalid worker UID")
	}
	gid, e := strconv.ParseUint(os.Getenv("DM_WORKER_GID"), 10, 32)
	if e != nil {
		return errors.New("invalid worker GID")
	}
	if os.Getenv("RESTIC_REPOSITORY") != "/repository" {
		return errors.New("worker repository must be the explicit /repository mount")
	}
	cmd := exec.Command("restic", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "HOME=/tmp", "RESTIC_CACHE_DIR=/tmp/restic-cache")
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}, AmbientCaps: []uintptr{2}}
	return cmd.Run()
}
