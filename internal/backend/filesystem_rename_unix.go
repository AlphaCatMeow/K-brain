//go:build darwin || linux

package backend

import (
	"errors"
	"os"
	"path/filepath"
)

func renameFilesystemExclusive(root *os.Root, from, to string) error {
	source, err := root.Open(filepath.Dir(from))
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := root.Open(filepath.Dir(to))
	if err != nil {
		return err
	}
	defer target.Close()
	for _, directory := range []*os.File{source, target} {
		info, err := directory.Stat()
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return errors.New("rename parent must be a directory")
		}
	}
	return renameFilesystemAt(int(source.Fd()), filepath.Base(from), int(target.Fd()), filepath.Base(to))
}
