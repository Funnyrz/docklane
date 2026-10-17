// Package restoreworker confines privileged metadata restoration to explicit mounts.
package restoreworker

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

type Node struct {
	Path string `json:"path"`
	Type string `json:"type"`
	UID  uint32 `json:"uid"`
	GID  uint32 `json:"gid"`
	Mode uint32 `json:"mode"`
	Size uint64 `json:"size"`
}
type Spec struct {
	Snapshot string `json:"snapshot"`
	Source   string `json:"source,omitempty"`
	Nodes    []Node `json:"nodes"`
}

var snapshotID = regexp.MustCompile(`^[0-9a-f]{64}$`)

func cleanAbsolute(p string) bool {
	return strings.HasPrefix(p, "/") && path.Clean(p) == p && !strings.ContainsRune(p, 0)
}
func (s Spec) Validate() error {
	if !snapshotID.MatchString(s.Snapshot) {
		return errors.New("restore worker requires an immutable full snapshot ID")
	}
	if s.Source != "" && (!cleanAbsolute(s.Source) || s.Source == "/") {
		return errors.New("invalid restore source subtree")
	}
	if len(s.Nodes) == 0 || len(s.Nodes) > 1000000 {
		return errors.New("invalid restore node count")
	}
	types := map[string]string{}
	for _, n := range s.Nodes {
		if !cleanAbsolute(n.Path) {
			return errors.New("restore metadata contains an unsafe relative path")
		}
		if n.Type != "file" && n.Type != "dir" && n.Type != "symlink" {
			return fmt.Errorf("unsupported restore node type %q", n.Type)
		}
		if _, exists := types[n.Path]; exists {
			return errors.New("duplicate restore metadata path")
		}
		types[n.Path] = n.Type
	}
	for _, n := range s.Nodes {
		for parent := path.Dir(n.Path); parent != "/"; parent = path.Dir(parent) {
			if typ, exists := types[parent]; exists && typ != "dir" {
				return errors.New("restore metadata traverses a non-directory ancestor")
			}
		}
	}
	return nil
}
func verifyAncestors(root string, n Node) error {
	current := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(n.Path, "/")))
	for {
		info, e := os.Lstat(current)
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		if e == nil && info.Mode()&os.ModeSymlink != 0 && (current != filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(n.Path, "/"))) || n.Type != "symlink") {
			return errors.New("restore path traverses an existing symlink")
		}
		if current == root {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			return errors.New("restore path escaped target")
		}
		current = parent
	}
	return nil
}
func verifyMetadata(root string, s Spec) error {
	for _, n := range s.Nodes {
		if e := verifyAncestors(root, n); e != nil {
			return e
		}
		p := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(n.Path, "/")))
		info, e := os.Lstat(p)
		if e != nil {
			return fmt.Errorf("restored metadata missing: %w", e)
		}
		switch n.Type {
		case "file":
			if !info.Mode().IsRegular() || uint64(info.Size()) != n.Size {
				return errors.New("restored file type or length differs")
			}
		case "dir":
			if !info.IsDir() {
				return errors.New("restored directory type differs")
			}
		case "symlink":
			if info.Mode()&os.ModeSymlink == 0 {
				return errors.New("restored symlink type differs")
			}
		}
		if n.Type != "symlink" {
			mask := os.FileMode(0777) | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
			if info.Mode()&mask != os.FileMode(n.Mode)&mask {
				return errors.New("restored permissions differ from snapshot")
			}
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != n.UID || st.Gid != n.GID {
			return errors.New("restored ownership differs from snapshot")
		}
	}
	return nil
}
