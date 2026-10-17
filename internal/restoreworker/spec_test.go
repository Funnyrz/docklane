package restoreworker

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestSpecificationRejectsMutableSnapshotsAndEscapingPaths(t *testing.T) {
	s := Spec{Snapshot: strings.Repeat("a", 64), Nodes: []Node{{Path: "/data", Type: "dir"}, {Path: "/data/file", Type: "file"}}}
	if e := s.Validate(); e != nil {
		t.Fatal(e)
	}
	s.Snapshot = "latest"
	if e := s.Validate(); e == nil {
		t.Fatal("mutable snapshot accepted")
	}
	s.Snapshot = strings.Repeat("a", 64)
	s.Nodes[1].Path = "/data/../escape"
	if e := s.Validate(); e == nil {
		t.Fatal("traversal accepted")
	}
	s.Nodes[1].Path = "/data/file"
	s.Nodes[0].Type = "symlink"
	if e := s.Validate(); e == nil {
		t.Fatal("symlink ancestor accepted")
	}
}
func TestMetadataVerificationIncludesOwnershipModeAndSymlinkSafety(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "file")
	if e := os.WriteFile(p, []byte("data"), 0640); e != nil {
		t.Fatal(e)
	}
	info, _ := os.Lstat(p)
	stat := info.Sys().(*syscall.Stat_t)
	spec := Spec{Snapshot: strings.Repeat("b", 64), Nodes: []Node{{Path: "/file", Type: "file", UID: stat.Uid, GID: stat.Gid, Mode: uint32(info.Mode()), Size: 4}}}
	if e := verifyMetadata(root, spec); e != nil {
		t.Fatal(e)
	}
	os.Chmod(p, 0600)
	if e := verifyMetadata(root, spec); e == nil {
		t.Fatal("wrong mode accepted")
	}
	os.Remove(p)
	os.Symlink(filepath.Join(t.TempDir(), "outside"), p)
	if e := verifyMetadata(root, spec); e == nil {
		t.Fatal("symlink substitution accepted")
	}
}
