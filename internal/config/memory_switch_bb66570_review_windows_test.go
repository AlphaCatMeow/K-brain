//go:build windows

package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Review (bb66570), passing table rows for a switch file that exists but cannot be read
// (locked by another handle that still allows deletion). On+fail is
// TestReviewMemorySwitchFailedSaveOfUnreadableSwitchTurnsMemoryOn; on over a holder that
// forbids deletion is TestReviewMemorySwitchLockedSwitchTurnOnReportsError.
func TestReviewMemorySwitchSaveStateTableLocked(t *testing.T) {
	offBytes := []byte("{\n  \"enabled\": false\n}")
	for _, tc := range []struct {
		targetOn, writeFails bool
		wantOn, wantFile     bool
	}{
		{targetOn: true, writeFails: false, wantOn: true, wantFile: false},
		{targetOn: false, writeFails: false, wantOn: false, wantFile: true},
		{targetOn: false, writeFails: true, wantOn: false, wantFile: true},
	} {
		for _, via := range []string{"Save", "SaveFile"} {
			name := fmt.Sprintf("locked/on=%v/fail=%v/%s", tc.targetOn, tc.writeFails, via)
			home := t.TempDir()
			t.Setenv("LIVEAGENT_HOME", home)
			p := filepath.Join(home, "config.json")
			side := memorySwitchPath(p)
			if tc.writeFails {
				if err := os.Mkdir(p, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(p, []byte(reviewHealthyConfig), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(side, offBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			unlock := lockSwitchFile(t, side, true)
			var probe Config
			probe.loadMemorySwitch(p)
			if probe.Memory.IsEnabled() {
				unlock()
				t.Fatalf("%s: precondition: locked switch should load as off", name)
			}
			target := tc.targetOn
			cfg := &Config{Memory: MemoryConfig{Enabled: &target}, allowEmptySave: true}
			var err error
			if via == "Save" {
				err = cfg.Save()
			} else {
				err = cfg.SaveFile(p)
			}
			unlock()
			if (err != nil) != tc.writeFails {
				t.Fatalf("%s: save err=%v want failure=%v", name, err, tc.writeFails)
			}
			got, readErr := os.ReadFile(side)
			if exists := !os.IsNotExist(readErr); exists != tc.wantFile {
				t.Fatalf("%s: switch file exists=%v want %v", name, exists, tc.wantFile)
			}
			if tc.wantFile && !bytes.Equal(got, offBytes) {
				t.Fatalf("%s: switch file = %q (%v), want untouched", name, got, readErr)
			}
			var reloaded Config
			reloaded.loadMemorySwitch(p)
			if reloaded.Memory.IsEnabled() != tc.wantOn {
				t.Fatalf("%s: next load on=%v want %v", name, reloaded.Memory.IsEnabled(), tc.wantOn)
			}
		}
	}
}
