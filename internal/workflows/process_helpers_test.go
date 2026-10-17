package workflows

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

func handleProcessHelper() bool {
	mode := os.Getenv("WORKFLOW_PROCESS_HELPER")
	if mode == "" {
		return false
	}
	file := os.Getenv("WORKFLOW_PROCESS_PIDFILE")
	if e := os.WriteFile(file, []byte(fmt.Sprint(os.Getpid())), 0600); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	executable, e := os.Executable()
	if e != nil {
		os.Exit(1)
	}
	childEnv := append(os.Environ(), "WORKFLOW_PROCESS_HELPER=leaf", "WORKFLOW_PROCESS_PIDFILE="+os.Getenv("WORKFLOW_PROCESS_CHILD_PIDFILE"))
	switch mode {
	case "owner":
		_, e = run(context.Background(), "", childEnv, executable, "workflow-process-helper", os.Getenv("WORKFLOW_PROCESS_TOKEN"))
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		return true
	case "tree":
		// This descendant intentionally inherits the command's process group and
		// pipes. Cancel must kill both processes rather than just the direct child.
		child := exec.Command(executable, "workflow-process-helper", os.Getenv("WORKFLOW_PROCESS_TOKEN"))
		child.Env = childEnv
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if e = child.Start(); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
	case "leaf":
	default:
		os.Exit(1)
	}
	for {
		time.Sleep(time.Hour)
	}
}
