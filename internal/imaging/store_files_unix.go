//go:build !windows

package imaging

import "os"

func replaceStoreFile(source, target string) error { return os.Rename(source, target) }
func syncStoreDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
