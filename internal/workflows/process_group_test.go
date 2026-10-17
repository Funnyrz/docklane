//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package workflows

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func waitFixturePID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, e := os.ReadFile(path)
		if e == nil {
			pid, e := strconv.Atoi(strings.TrimSpace(string(b)))
			if e == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helper PID was not reported: %s", path)
	return 0
}
func fixtureRunning(pid int) bool {
	if runtime.GOOS == "linux" {
		b, e := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if e != nil {
			return !os.IsNotExist(e)
		}
		end := strings.LastIndex(string(b), ")")
		if end < 0 || len(b) <= end+2 {
			return true
		}
		state := b[end+2]
		return state != 'Z' && state != 'X'
	}
	b, e := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if e != nil {
		return false
	}
	state := strings.TrimSpace(string(b))
	return state != "" && !strings.HasPrefix(state, "Z")
}
func assertFixtureExited(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !fixtureRunning(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("owned helper process %d is still running", pid)
}
func cleanupOwnedFixture(t *testing.T, pid int, token string, group bool) {
	t.Helper()
	if !fixtureRunning(pid) {
		return
	}
	var command string
	if runtime.GOOS == "linux" {
		b, e := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
		if e != nil {
			return
		}
		command = string(b)
	} else {
		b, e := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
		if e != nil {
			return
		}
		command = string(b)
	}
	if !strings.Contains(command, token) || !strings.Contains(command, "workflow-process-helper") {
		t.Errorf("refusing to clean a process outside this fixture: %d", pid)
		return
	}
	target := pid
	if group {
		target = -pid
	}
	_ = syscall.Kill(target, syscall.SIGKILL)
}
func TestControlledCommandCancellationKillsOwnedProcessGroup(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			leaderFile := filepath.Join(dir, "leader.pid")
			childFile := filepath.Join(dir, "child.pid")
			token := id()
			exe, e := os.Executable()
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			if mode == "deadline" {
				ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, e := run(ctx, "", []string{"WORKFLOW_PROCESS_HELPER=tree", "WORKFLOW_PROCESS_PIDFILE=" + leaderFile, "WORKFLOW_PROCESS_CHILD_PIDFILE=" + childFile, "WORKFLOW_PROCESS_TOKEN=" + token}, exe, "workflow-process-helper", token)
				done <- e
			}()
			leader := waitFixturePID(t, leaderFile)
			child := waitFixturePID(t, childFile)
			t.Cleanup(func() { cleanupOwnedFixture(t, leader, token, true); cleanupOwnedFixture(t, child, token, false) })
			started := time.Now()
			if mode == "cancel" {
				cancel()
			}
			select {
			case e = <-done:
				expected := context.Canceled
				if mode == "deadline" {
					expected = context.DeadlineExceeded
				}
				if !errors.Is(e, expected) {
					t.Fatalf("got %v, expected %v", e, expected)
				}
			case <-time.After(6 * time.Second):
				t.Fatal("command wait did not finish after cancellation")
			}
			if time.Since(started) > 6*time.Second {
				t.Fatal("cancellation wait was not bounded")
			}
			assertFixtureExited(t, leader)
			assertFixtureExited(t, child)
		})
	}
}
func TestControlledCommandHasBoundedPipeWait(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "unused")
	configureCommand(cmd)
	if cmd.WaitDelay != 2*time.Second {
		t.Fatal("missing bounded subprocess pipe wait", cmd.WaitDelay)
	}
	if cmd.Cancel == nil {
		t.Fatal("missing command cancellation")
	}
}
