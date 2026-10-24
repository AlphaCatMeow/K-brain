package backend

import "golang.org/x/sys/unix"

func renameFilesystemAt(fromFD int, from string, toFD int, to string) error {
	return unix.RenameatxNp(fromFD, from, toFD, to, unix.RENAME_EXCL)
}
