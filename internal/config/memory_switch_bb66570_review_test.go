package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const reviewHealthyConfig = `{"providers":{"x":{"api":"openai-completions","models":[{"id":"m"}]}}}`

// reviewSwitchState is one prior state of the switch file; nil bytes means no file.
type reviewSwitchState struct {
	name   string
	bytes  []byte
	loadOn bool // what the switch file means over a config.json without "memory"
}

var reviewSwitchStates = []reviewSwitchState{
	{"absent", nil, true},
	{"written off", []byte("{\n  \"enabled\": false\n}"), false},
	{"hand-written on", []byte(`{"enabled":true}`), true},
	{"damaged", []byte("{not json"), false},
}

// Review (bb66570), passing table: every prior switch state x target x config.json write
// outcome, through Save and SaveFile. A failed save leaves the switch file byte-identical;
// a successful one leaves no file for on, and an off file for off (the old bytes when the
// prior state already loaded as off). The next Load and LoadFile agree with the disk.
func TestReviewMemorySwitchSaveStateTable(t *testing.T) {
	for _, prev := range reviewSwitchStates {
		for _, targetOn := range []bool{true, false} {
			for _, writeFails := range []bool{false, true} {
				for _, via := range []string{"Save", "SaveFile"} {
					name := fmt.Sprintf("%s/on=%v/fail=%v/%s", prev.name, targetOn, writeFails, via)
					home := t.TempDir()
					t.Setenv("LIVEAGENT_HOME", home)
					p := filepath.Join(home, "config.json")
					side := memorySwitchPath(p)
					if err := os.WriteFile(p, []byte(reviewHealthyConfig), 0o600); err != nil {
						t.Fatal(err)
					}
					if prev.bytes != nil {
						if err := os.WriteFile(side, prev.bytes, 0o600); err != nil {
							t.Fatal(err)
						}
					}
					cfg, err := LoadFile(p)
					if err != nil {
						t.Fatal(err)
					}
					if cfg.Memory.IsEnabled() != prev.loadOn {
						t.Fatalf("%s: precondition load on=%v want %v", name, cfg.Memory.IsEnabled(), prev.loadOn)
					}
					target := targetOn
					cfg.Memory = MemoryConfig{Enabled: &target}
					if writeFails {
						// A directory at config.json makes the publish fail after the switch step.
						if err := os.Remove(p); err != nil {
							t.Fatal(err)
						}
						if err := os.Mkdir(p, 0o700); err != nil {
							t.Fatal(err)
						}
					}
					if via == "Save" {
						err = cfg.Save()
					} else {
						err = cfg.SaveFile(p)
					}
					if (err != nil) != writeFails {
						t.Fatalf("%s: save err=%v, want failure=%v", name, err, writeFails)
					}
					if writeFails {
						// The real failure leaves the old config.json; put it back for the reload.
						if err := os.Remove(p); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(p, []byte(reviewHealthyConfig), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					got, readErr := os.ReadFile(side)
					exists := !os.IsNotExist(readErr)
					if readErr != nil && exists {
						t.Fatalf("%s: switch file unreadable after save: %v", name, readErr)
					}
					wantOn := prev.loadOn
					switch {
					case writeFails:
						if exists != (prev.bytes != nil) || !bytes.Equal(got, prev.bytes) {
							t.Fatalf("%s: failed save changed the switch file: exists=%v %q, want %q", name, exists, got, prev.bytes)
						}
					case targetOn:
						wantOn = true
						if exists {
							t.Fatalf("%s: memory on left a switch file %q", name, got)
						}
					default:
						wantOn = false
						if !exists {
							t.Fatalf("%s: memory off left no switch file", name)
						}
						if !prev.loadOn && !bytes.Equal(got, prev.bytes) {
							t.Fatalf("%s: unchanged off switch was rewritten: %q -> %q", name, prev.bytes, got)
						}
						var m MemoryConfig
						if prev.loadOn && (json.Unmarshal(got, &m) != nil || m.Enabled == nil || *m.Enabled) {
							t.Fatalf("%s: off switch file = %q", name, got)
						}
					}
					data, _ := os.ReadFile(p)
					if strings.Contains(string(data), `"memory"`) {
						t.Fatalf("%s: config.json carries the memory key:\n%s", name, data)
					}
					viaFile, err := LoadFile(p)
					if err != nil {
						t.Fatal(err)
					}
					viaLoad, err := Load()
					if err != nil {
						t.Fatal(err)
					}
					if viaFile.Memory.IsEnabled() != wantOn || viaLoad.Memory.IsEnabled() != wantOn {
						t.Fatalf("%s: next LoadFile on=%v Load on=%v, want %v", name, viaFile.Memory.IsEnabled(), viaLoad.Memory.IsEnabled(), wantOn)
					}
				}
			}
		}
	}
}

// Review (bb66570), passing: a legacy "memory" off in config.json and no switch file, through
// the settings-store flow (LoadFile, change, SaveFile).
func TestReviewMemorySwitchLegacyKeyUserTransitions(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	legacy := `{"providers":{"x":{"api":"openai-completions","models":[{"id":"m"}]}},"memory":{"enabled":false}}`
	for _, tc := range []struct {
		name     string
		change   func(*Config)
		wantOn   bool
		wantSide bool
	}{
		{"user turns memory on", func(c *Config) { on := true; c.Memory = MemoryConfig{Enabled: &on} }, true, false},
		{"user saves an unrelated setting", func(c *Config) { c.Language = "zh" }, false, true},
	} {
		dir := t.TempDir()
		p := filepath.Join(dir, "config.json")
		side := memorySwitchPath(p)
		if err := os.WriteFile(p, []byte(legacy), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadFile(p)
		if err != nil || cfg.Memory.IsEnabled() {
			t.Fatalf("%s: precondition legacy off: err=%v", tc.name, err)
		}
		tc.change(cfg)
		if err := cfg.SaveFile(p); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(p)
		if strings.Contains(string(data), `"memory"`) {
			t.Fatalf("%s: legacy key survived the save:\n%s", tc.name, data)
		}
		if _, err := os.Stat(side); (err == nil) != tc.wantSide {
			t.Fatalf("%s: switch file present=%v want %v", tc.name, err == nil, tc.wantSide)
		}
		again, err := LoadFile(p)
		if err != nil || again.Memory.IsEnabled() != tc.wantOn {
			t.Fatalf("%s: next load want on=%v (%v)", tc.name, tc.wantOn, err)
		}
	}
}

// Review (bb66570), passing: the unchanged-switch skip inside the Save that each Load
// recovery branch performs, for every prior switch state.
func TestReviewMemorySwitchLoadRecoveryWithEachSwitchState(t *testing.T) {
	for _, prev := range reviewSwitchStates {
		for _, branch := range []struct {
			name, config, backup string
		}{
			{"missing config", "", ""},
			{"empty config restored from backup", `{}`, reviewHealthyConfig},
			{"empty config without backup", `{}`, ""},
		} {
			name := prev.name + "/" + branch.name
			home := t.TempDir()
			t.Setenv("LIVEAGENT_HOME", home)
			p := filepath.Join(home, "config.json")
			side := memorySwitchPath(p)
			if branch.config != "" {
				if err := os.WriteFile(p, []byte(branch.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if branch.backup != "" {
				if err := os.WriteFile(p+".bak", []byte(branch.backup), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if prev.bytes != nil {
				if err := os.WriteFile(side, prev.bytes, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := Load()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if cfg.Memory.IsEnabled() != prev.loadOn {
				t.Fatalf("%s: recovery on=%v want %v", name, cfg.Memory.IsEnabled(), prev.loadOn)
			}
			got, readErr := os.ReadFile(side)
			if !prev.loadOn && (readErr != nil || !bytes.Equal(got, prev.bytes)) {
				t.Fatalf("%s: off switch file changed by recovery save: %q %v", name, got, readErr)
			}
			if prev.loadOn && prev.bytes == nil && !os.IsNotExist(readErr) {
				t.Fatalf("%s: recovery save created a switch file: %q", name, got)
			}
			again, err := Load()
			if err != nil || again.Memory.IsEnabled() != prev.loadOn {
				t.Fatalf("%s: second load want on=%v (%v)", name, prev.loadOn, err)
			}
		}
	}
}

// Review (bb66570), failing: the migration rule says a legacy "memory" off in config.json
// still counts. LoadFile honors it, but Load sends a config with no providers or models
// through the empty-config recovery branch, which rebuilds Default() keeping only Language
// and MCPServers and then saves. The off value is dropped from memory and from disk, so
// memory stays on from then on. This is the fixture TestMemorySwitchMigratesLegacyConfigValue uses.
// The .bak branch has the same gap when the backup predates the off.
func TestReviewMemorySwitchLegacyOffSurvivesEmptyConfigLoad(t *testing.T) {
	for _, backup := range []string{"", reviewHealthyConfig} {
		name := map[bool]string{true: "without backup", false: "restored from backup"}[backup == ""]
		home := t.TempDir()
		t.Setenv("LIVEAGENT_HOME", home)
		p := filepath.Join(home, "config.json")
		if err := os.WriteFile(p, []byte(`{"memory":{"enabled":false}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if backup != "" {
			if err := os.WriteFile(p+".bak", []byte(backup), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if cfg, err := LoadFile(p); err != nil || cfg.Memory.IsEnabled() {
			t.Fatalf("%s: precondition: LoadFile should honor the legacy off (err=%v)", name, err)
		}
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		_, statErr := os.Stat(memorySwitchPath(p))
		if cfg.Memory.IsEnabled() {
			t.Errorf("%s: Load dropped the legacy memory off; switch file exists=%v", name, statErr == nil)
		}
	}
}
