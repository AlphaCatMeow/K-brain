//go:build windows

package config

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// lockSwitchFile opens the switch file the way backup, sync and AV tools often do and takes
// an exclusive byte-range lock: reads fail with a lock violation. shareDelete decides whether
// the holder still lets others delete the file.
func lockSwitchFile(t *testing.T, side string, shareDelete bool) (unlock func()) {
	t.Helper()
	name, err := windows.UTF16PtrFromString(side)
	if err != nil {
		t.Fatal(err)
	}
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE)
	if shareDelete {
		share |= windows.FILE_SHARE_DELETE
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, share, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1<<20, 0, ol); err != nil {
		windows.CloseHandle(h)
		t.Fatal(err)
	}
	if _, err := os.ReadFile(side); err == nil || os.IsNotExist(err) {
		t.Fatalf("precondition: locked switch file should be unreadable, got %v", err)
	}
	return func() {
		_ = windows.UnlockFileEx(h, 0, 1<<20, 0, ol)
		_ = windows.CloseHandle(h)
	}
}

// Review (f75f250): an unreadable switch file loads as off. Turning memory on removes the
// file before config.json is written. When that write then fails, the restore callback only
// knows how to put back a file it managed to read, so the off switch is gone even though
// SaveFile returned an error: the next start comes up with memory on.
func TestReviewMemorySwitchFailedSaveOfUnreadableSwitchTurnsMemoryOn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", home)
	p := filepath.Join(home, "config.json")
	side := memorySwitchPath(p)
	if err := os.WriteFile(side, []byte("{\n  \"enabled\": false\n}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A directory at config.json makes the config write fail after the switch step,
	// as in TestReviewMemorySwitchRestoredWhenConfigWriteFails.
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}

	unlock := lockSwitchFile(t, side, true)
	var loaded Config
	loaded.loadMemorySwitch(p)
	if loaded.Memory.IsEnabled() {
		unlock()
		t.Fatal("precondition: unreadable switch file should load as off")
	}
	on := true
	cfg := &Config{Memory: MemoryConfig{Enabled: &on}, allowEmptySave: true}
	saveErr := cfg.SaveFile(p)
	unlock()
	if saveErr == nil {
		t.Fatal("precondition: SaveFile should fail with a directory at config.json")
	}

	var reloaded Config
	reloaded.loadMemorySwitch(p)
	if reloaded.Memory.IsEnabled() {
		_, statErr := os.Stat(side)
		t.Fatalf("SaveFile failed (%v) but memory now loads as on; switch file gone=%v", saveErr, os.IsNotExist(statErr))
	}
}

// Review (f75f250), passing check: when the lock holder does not allow deletion, turning
// memory on returns the remove error and the off switch stays, so nothing diverges silently.
func TestReviewMemorySwitchLockedSwitchTurnOnReportsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", home)
	p := filepath.Join(home, "config.json")
	off := false
	if err := (&Config{Language: "en", Memory: MemoryConfig{Enabled: &off}}).SaveFile(p); err != nil {
		t.Fatal(err)
	}
	side := memorySwitchPath(p)
	unlock := lockSwitchFile(t, side, false)
	cfg, err := LoadFile(p)
	if err != nil {
		unlock()
		t.Fatal(err)
	}
	on := true
	cfg.Memory = MemoryConfig{Enabled: &on}
	saveErr := cfg.SaveFile(p)
	unlock()
	if saveErr == nil {
		t.Fatal("turning memory on over a locked switch file reported success")
	}
	reloaded, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Memory.IsEnabled() {
		t.Fatalf("SaveFile failed (%v) but memory loads as on", saveErr)
	}
}

// Review (f75f250): while memory is off, every save rewrites the switch file even when the
// switch did not change. With the file held open (here locked, which also makes it load as
// off), a save that only changes another setting fails on the switch rename.
func TestReviewMemorySwitchHeldOffSwitchBlocksUnrelatedSave(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", home)
	p := filepath.Join(home, "config.json")
	off := false
	if err := (&Config{Language: "en", Memory: MemoryConfig{Enabled: &off}}).SaveFile(p); err != nil {
		t.Fatal(err)
	}
	unlock := lockSwitchFile(t, memorySwitchPath(p), false)
	cfg, err := LoadFile(p)
	if err != nil {
		unlock()
		t.Fatal(err)
	}
	cfg.Language = "zh"
	saveErr := cfg.SaveFile(p)
	unlock()
	if saveErr != nil {
		t.Fatalf("language-only save failed on the unchanged memory switch: %v", saveErr)
	}
}
