//go:build windows

package imaging

import "golang.org/x/sys/windows"

// MoveFileEx replaces an existing checkpoint and flushes the move before return.
// All names are in the same directory/volume; no copy/delete fallback is used.
func replaceStoreFile(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// Windows does not support fsync on directory handles; replacement durability is
// provided by MOVEFILE_WRITE_THROUGH above, and file contents by File.Sync.
func syncStoreDirectory(string) error { return nil }
