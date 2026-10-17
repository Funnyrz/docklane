package safepath

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func canonical(p string) (string, error) {
	if p == "" || !filepath.IsAbs(p) || strings.ContainsRune(p, 0) {
		return "", errors.New("absolute path required")
	}
	p = filepath.Clean(p)
	cur := p
	var missing []string
	for {
		r, e := filepath.EvalSymlinks(cur)
		if e == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				r = filepath.Join(r, missing[i])
			}
			return r, nil
		}
		if !os.IsNotExist(e) {
			return "", e
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", e
		}
		missing = append(missing, filepath.Base(cur))
		cur = parent
	}
}
func within(root, p string) bool {
	r, e := filepath.Rel(root, p)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}
func Resolve(p string, allowed []string) (string, error) {
	r, e := canonical(p)
	if e != nil {
		return "", e
	}
	for _, root := range allowed {
		a, er := canonical(root)
		if er != nil {
			continue
		}
		if a == string(filepath.Separator) {
			continue
		}
		if within(a, r) {
			return r, nil
		}
	}
	return "", fmt.Errorf("path is outside explicitly authorized directories")
}
func Disjoint(a, b string) error {
	x, e := canonical(a)
	if e != nil {
		return e
	}
	y, e := canonical(b)
	if e != nil {
		return e
	}
	if within(x, y) || within(y, x) {
		return errors.New("source and repository/target paths must not contain one another")
	}
	return nil
}
func AtomicWrite(p string, b []byte, mode os.FileMode) error {
	f, e := os.CreateTemp(filepath.Dir(p), ".dm-write-")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Rename(tmp, p); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(p))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
