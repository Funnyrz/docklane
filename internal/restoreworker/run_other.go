//go:build !linux

package restoreworker

import "errors"

func Run(string) error { return errors.New("restore worker is supported only on Linux") }
