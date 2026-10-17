//go:build linux

package workflows

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestLinuxControlledCommandDiesWhenParentIsKilled(t *testing.T) {
	dir := t.TempDir()
	ownerFile := filepath.Join(dir, "owner.pid")
	childFile := filepath.Join(dir, "controlled.pid")
	token := id()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	owner := exec.Command(exe, "workflow-process-helper", token)
	owner.Env = append(os.Environ(), "WORKFLOW_PROCESS_HELPER=owner", "WORKFLOW_PROCESS_PIDFILE="+ownerFile, "WORKFLOW_PROCESS_CHILD_PIDFILE="+childFile, "WORKFLOW_PROCESS_TOKEN="+token)
	owner.WaitDelay = 2 * time.Second
	if e = owner.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if owner.ProcessState == nil {
			_ = owner.Process.Kill()
			_ = owner.Wait()
		}
	})
	controlled := waitFixturePID(t, childFile)
	t.Cleanup(func() { cleanupOwnedFixture(t, controlled, token, true) })
	if !fixtureRunning(controlled) {
		t.Fatal("controlled child exited before parent kill")
	}
	if e = owner.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	_ = owner.Wait()
	assertFixtureExited(t, controlled)
}
func TestLinuxControlledCommandDeathSignalAndGroup(t *testing.T) {
	cmd := exec.Command("unused")
	configureCommand(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid || cmd.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Fatal("controlled CLI lacks parent-death signal or its own process group")
	}
}
