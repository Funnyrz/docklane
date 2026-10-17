package database

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lock prevents a second process from compensating a live process's tasks.
func Lock(dir string) (*os.File, error) {
	if e := privateDir(dir); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(dir, "server.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, fmt.Errorf("data directory is in use by another Docklane process: %w", e)
	}
	if e = f.Truncate(0); e != nil {
		f.Close()
		return nil, e
	}
	if _, e = fmt.Fprintf(f, "%d\n", os.Getpid()); e != nil {
		f.Close()
		return nil, e
	}
	return f, nil
}
func privateDir(dir string) error {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	actual, e := filepath.EvalSymlinks(dir)
	if e != nil {
		return e
	}
	actual, e = filepath.Abs(actual)
	if e != nil {
		return e
	}
	if actual == string(filepath.Separator) {
		return fmt.Errorf("data directory cannot resolve to filesystem root")
	}
	return os.Chmod(actual, 0700)
}
