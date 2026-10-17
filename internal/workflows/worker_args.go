package workflows

import (
	"errors"
	"path/filepath"
	"strings"
)

func validateWorkerArgs(args []string) error {
	if len(args) < 4 || len(args) > 20000 || args[0] != "backup" || args[1] != "--json" {
		return errors.New("worker only accepts a JSON backup with explicit mounted sources")
	}
	manifest, sources := false, 0
	for i := 2; i < len(args); i++ {
		arg := args[i]
		if strings.ContainsRune(arg, 0) {
			return errors.New("invalid worker argument")
		}
		switch arg {
		case "--tag":
			i++
			if i >= len(args) || !strings.HasPrefix(args[i], "dm-policy:") {
				return errors.New("invalid backup policy tag")
			}
		case "--exclude":
			i++
			if i >= len(args) || strings.HasPrefix(args[i], "-") {
				return errors.New("invalid backup exclusion")
			}
		case "/manifest/manifest.json":
			manifest = true
		default:
			if !strings.HasPrefix(arg, "/sources/") || filepath.Clean(arg) != arg {
				return errors.New("worker may only read its explicit source mounts")
			}
			name := strings.TrimPrefix(arg, "/sources/")
			if name == "" {
				return errors.New("missing source mount index")
			}
			for _, r := range name {
				if r < '0' || r > '9' {
					return errors.New("invalid source mount index")
				}
			}
			sources++
		}
	}
	if !manifest || sources == 0 {
		return errors.New("worker backup requires a manifest and at least one source")
	}
	return nil
}
