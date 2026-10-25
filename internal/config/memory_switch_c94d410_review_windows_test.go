//go:build windows

package config

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// A switch file that exists but cannot be read (here another handle holds an exclusive
// byte-range lock, as backup, sync or AV tools can) means off like a damaged one, so the
// next save must not delete it.
func TestReviewMemorySwitchUnreadableFileKeepsMemoryOff(t *testing.T) {
	// The load and save logs resolve the data root; keep them out of the real one.
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	off := false
	if err := (&Config{Language: "en", Memory: MemoryConfig{Enabled: &off}}).SaveFile(p); err != nil {
		t.Fatal(err)
	}
	side := memorySwitchPath(filepath.Join(dir, "config.json"))
	f, err := os.OpenFile(side, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1<<20, 0, ol); err != nil {
		t.Fatal(err)
	}
	_, readErr := os.ReadFile(side)
	cfg, loadErr := LoadFile(p)
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1<<20, 0, ol)
	f.Close()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if cfg.Memory.IsEnabled() {
		// Any later settings save writes the loaded (on) value back.
		saveErr := cfg.SaveFile(p)
		_, statErr := os.Stat(side)
		t.Fatalf("memory off read as on while the switch file was locked (read err: %v); next SaveFile err=%v, switch file gone=%v", readErr, saveErr, os.IsNotExist(statErr))
	}
}
