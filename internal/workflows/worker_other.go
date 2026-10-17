//go:build !linux

package workflows

import "errors"

func RunWorker(_ []string) error {
	return errors.New("backup worker identity/capabilities are supported only on Linux")
}
