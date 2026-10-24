//go:build !darwin && !linux

package backend

import (
	"errors"
	"os"
)

func renameFilesystemExclusive(_ *os.Root, _, _ string) error {
	return errors.New("exclusive file rename is unavailable on this platform")
}
