//go:build linux

package restoreworker

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// Run is an internal image entry point, never an HTTP command execution API.
func Run(specPath string) error {
	if specPath != "/spec/spec.json" || os.Getenv("RESTIC_REPOSITORY") != "/repository" {
		return errors.New("restore worker requires fixed spec and repository mounts")
	}
	f, e := os.Open(specPath)
	if e != nil {
		return e
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, (16<<20)+1))
	dec.DisallowUnknownFields()
	var spec Spec
	if e = dec.Decode(&spec); e != nil {
		return e
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return errors.New("invalid restore specification size or trailing data")
	}
	if e = spec.Validate(); e != nil {
		return e
	}
	root, e := os.Lstat("/target")
	if e != nil || !root.IsDir() || root.Mode()&os.ModeSymlink != 0 {
		return errors.New("restore target must be an explicitly mounted directory")
	}
	for _, n := range spec.Nodes {
		if e = verifyAncestors("/target", n); e != nil {
			return e
		}
	}
	uid, e := strconv.ParseUint(os.Getenv("DM_WORKER_UID"), 10, 32)
	if e != nil {
		return errors.New("invalid restore worker UID")
	}
	gid, e := strconv.ParseUint(os.Getenv("DM_WORKER_GID"), 10, 32)
	if e != nil {
		return errors.New("invalid restore worker GID")
	}
	ref := spec.Snapshot
	if spec.Source != "" {
		ref += ":" + spec.Source
	}
	// Real Restic locking remains enabled; only repository/locks is writable.
	cmd := exec.Command("restic", "restore", ref, "--target", "/target", "--verify")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/tmp", "RESTIC_CACHE_DIR=/tmp/restic-cache", "RESTIC_REPOSITORY=/repository", "RESTIC_PASSWORD=" + os.Getenv("RESTIC_PASSWORD")}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}, AmbientCaps: []uintptr{0, 1, 2, 3, 4}}
	if e = cmd.Run(); e != nil {
		return errors.New("Restic restore or content verification failed")
	}
	if e = verifyMetadata("/target", spec); e != nil {
		return e
	}
	_, e = io.WriteString(os.Stdout, "\nDocklane: file content, permissions and ownership verified\n")
	return e
}
