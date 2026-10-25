package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Review probe (c94d410): a failed config.json write must leave the switch file as it was,
// for Save and SaveFile, whether the file existed before or not.
func TestReviewMemorySwitchRestoredWhenConfigWriteFails(t *testing.T) {
	on, off := true, false
	offFile := []byte("{\n  \"enabled\": false\n}")
	for _, tc := range []struct {
		name     string
		previous []byte // nil means no switch file
		enabled  *bool
	}{
		{"absent then off", nil, &off},
		{"present then on", offFile, &on},
		{"present then off", offFile, &off},
	} {
		for _, via := range []string{"Save", "SaveFile"} {
			home := t.TempDir()
			t.Setenv("LIVEAGENT_HOME", home)
			p := filepath.Join(home, "config.json")
			side := memorySwitchPath(filepath.Join(home, "config.json"))
			// A directory at config.json makes the final rename fail after the switch write.
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.previous != nil {
				if err := os.WriteFile(side, tc.previous, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := &Config{Memory: MemoryConfig{Enabled: tc.enabled}, allowEmptySave: true}
			var err error
			if via == "Save" {
				err = cfg.Save()
			} else {
				err = cfg.SaveFile(p)
			}
			if err == nil {
				t.Fatalf("%s/%s: config.json write unexpectedly succeeded", tc.name, via)
			}
			got, readErr := os.ReadFile(side)
			if tc.previous == nil {
				if !os.IsNotExist(readErr) {
					t.Fatalf("%s/%s: failed save left a switch file: %q %v", tc.name, via, got, readErr)
				}
				continue
			}
			if readErr != nil || !bytes.Equal(got, tc.previous) {
				t.Fatalf("%s/%s: switch file = %q (%v), want %q", tc.name, via, got, readErr, tc.previous)
			}
		}
	}
}

// Review probe (c94d410): the empty-overwrite refusal returns before the switch file is touched.
func TestReviewMemorySwitchRefusedSaveLeavesSwitchFile(t *testing.T) {
	on, off := true, false
	healthy := `{"providers":{"x":{"api":"openai-completions","models":[{"id":"m"}]}}}`
	for _, tc := range []struct {
		name    string
		present bool
		enabled *bool
	}{{"present, empty save with on", true, &on}, {"absent, empty save with off", false, &off}} {
		home := t.TempDir()
		t.Setenv("LIVEAGENT_HOME", home)
		p := filepath.Join(home, "config.json")
		side := memorySwitchPath(filepath.Join(home, "config.json"))
		if err := os.WriteFile(p, []byte(healthy), 0o600); err != nil {
			t.Fatal(err)
		}
		if tc.present {
			if err := os.WriteFile(side, []byte(`{"enabled":false}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := (&Config{Memory: MemoryConfig{Enabled: tc.enabled}}).Save(); err == nil {
			t.Fatalf("%s: empty overwrite was not refused", tc.name)
		}
		if _, err := os.Stat(side); (err == nil) != tc.present {
			t.Fatalf("%s: switch file present=%v after refusal, want %v", tc.name, err == nil, tc.present)
		}
	}
}

// Review probe (c94d410): "kn backend --config <file>" takes any file name, and the other
// sidecars (<file>.bak, <file>.live-mcp.json) are keyed by that name. The memory switch is
// keyed by the directory, so two configurations in one directory share one switch.
func TestReviewMemorySwitchBelongsToItsConfigFile(t *testing.T) {
	// The load and save logs resolve the data root; keep them out of the real one.
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	dir := t.TempDir()
	work, personal := filepath.Join(dir, "work.json"), filepath.Join(dir, "personal.json")
	off := false
	if err := (&Config{Language: "en"}).SaveFile(personal); err != nil {
		t.Fatal(err)
	}
	if err := (&Config{Language: "en", Memory: MemoryConfig{Enabled: &off}}).SaveFile(work); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(personal)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Memory.IsEnabled() {
		t.Fatal("turning memory off in work.json turned it off for personal.json, which never changed")
	}
}

// Review probe (c94d410): with both a legacy "memory" key in config.json and a switch file,
// the switch file decides; the key moves out of config.json on the next save.
func TestReviewMemorySwitchFileWinsOverLegacyKey(t *testing.T) {
	// The load and save logs resolve the data root; keep them out of the real one.
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	for _, tc := range []struct {
		name, legacy string
		side         bool
		wantOn       bool
	}{
		{"legacy off, switch off", `false`, true, false},
		{"legacy on, switch off", `true`, true, false},
		{"legacy off, no switch", `false`, false, false},
		{"legacy on, no switch", `true`, false, true},
	} {
		dir := t.TempDir()
		p := filepath.Join(dir, "config.json")
		if err := os.WriteFile(p, []byte(`{"memory":{"enabled":`+tc.legacy+`}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if tc.side {
			if err := os.WriteFile(memorySwitchPath(filepath.Join(dir, "config.json")), []byte(`{"enabled":false}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		cfg, err := LoadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Memory.IsEnabled() != tc.wantOn {
			t.Fatalf("%s: on=%v want %v", tc.name, cfg.Memory.IsEnabled(), tc.wantOn)
		}
		if err := cfg.SaveFile(p); err != nil {
			t.Fatal(err)
		}
		again, err := LoadFile(p)
		if err != nil || again.Memory.IsEnabled() != tc.wantOn {
			t.Fatalf("%s: after save on=%v want %v (%v)", tc.name, again.Memory.IsEnabled(), tc.wantOn, err)
		}
	}
}
