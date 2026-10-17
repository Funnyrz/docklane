package safepath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRejectsTraversalSymlinkAndRootAuthorization(t *testing.T) {
	d := t.TempDir()
	allowed := filepath.Join(d, "allowed")
	outside := filepath.Join(d, "outside")
	os.MkdirAll(allowed, 0700)
	os.MkdirAll(outside, 0700)
	os.Symlink(outside, filepath.Join(allowed, "escape"))
	for _, p := range []string{outside, filepath.Join(allowed, "escape", "new"), filepath.Join(allowed, "..", "outside"), "relative"} {
		if _, e := Resolve(p, []string{allowed}); e == nil {
			t.Fatalf("accepted %s", p)
		}
	}
	if _, e := Resolve(filepath.Join(allowed, "new", "dir"), []string{allowed}); e != nil {
		t.Fatal(e)
	}
	if _, e := Resolve(outside, []string{"/"}); e == nil {
		t.Fatal("root wildcard authorized")
	}
}
func TestDisjointAndAtomicWrite(t *testing.T) {
	d := t.TempDir()
	if e := Disjoint(d, filepath.Join(d, "repo")); e == nil {
		t.Fatal("recursive path accepted")
	}
	if e := Disjoint(filepath.Join(d, "source"), filepath.Join(d, "source-copy")); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(d, "config.json")
	if e := AtomicWrite(p, []byte(`{"safe":true}`), 0600); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(p)
	if string(b) != `{"safe":true}` {
		t.Fatal("write mismatch")
	}
}
