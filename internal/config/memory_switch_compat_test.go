package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Releases without the memory switch decode config.json with DisallowUnknownFields and
// refuse to start on a "memory" key, so the switch must never be written there.
func TestMemoryConfigNeverWrittenToConfigJSON(t *testing.T) {
	on, off := true, false
	for _, enabled := range []*bool{nil, &on, &off} {
		data, err := marshalConfig(&Config{Memory: MemoryConfig{Enabled: enabled}, allowEmptySave: true})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), `"memory"`) {
			t.Fatalf("enabled=%v wrote the memory key:\n%s", enabled != nil && *enabled, data)
		}
	}
}

func TestMemorySwitchFileHoldsOnlyTheOffState(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	side := filepath.Join(dir, memorySwitchFileName)
	off := false
	cfg := &Config{Memory: MemoryConfig{Enabled: &off}, allowEmptySave: true}
	if err := cfg.SaveFile(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(side); err != nil {
		t.Fatalf("memory off left no switch file: %v", err)
	}
	loaded, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Memory.IsEnabled() {
		t.Fatal("memory off did not survive LoadFile")
	}
	loaded.Memory = MemoryConfig{}
	loaded.allowEmptySave = true
	if err := loaded.SaveFile(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(side); !os.IsNotExist(err) {
		t.Fatalf("memory on kept the switch file: %v", err)
	}
	if again, err := LoadFile(p); err != nil || !again.Memory.IsEnabled() {
		t.Fatalf("memory on after reload: err=%v", err)
	}
}

// Builds of this branch before the side file wrote "memory" into config.json; that value
// still counts and moves to the side file on the next save.
func TestMemorySwitchMigratesLegacyConfigValue(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(`{"memory":{"enabled":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Memory.IsEnabled() {
		t.Fatal("legacy memory off in config.json was ignored")
	}
	cfg.allowEmptySave = true
	if err := cfg.SaveFile(p); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"memory"`) {
		t.Fatalf("legacy memory key survived a save:\n%s", data)
	}
	if again, err := LoadFile(p); err != nil || again.Memory.IsEnabled() {
		t.Fatalf("migrated memory off lost: err=%v", err)
	}
}

// The switch file exists only while memory is off, so a damaged one still means off.
func TestMemorySwitchDamagedFileStaysOff(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, memorySwitchFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(p)
	if err != nil {
		t.Fatalf("damaged switch file stopped the config from loading: %v", err)
	}
	if cfg.Memory.IsEnabled() {
		t.Fatal("damaged switch file turned memory back on")
	}
}

// Load rewrites config.json on its recovery paths; none of them may turn memory back on.
func TestMemorySwitchSurvivesLoadRecoveryPaths(t *testing.T) {
	provider := `{"providers":{"x":{"api":"openai-completions","models":[{"id":"m"}]}}}`
	for _, tc := range []struct {
		name   string
		config string // empty means config.json is missing
		backup string
	}{
		{"missing config", "", ""},
		{"empty config restored from backup", `{}`, provider},
		{"empty config without backup", `{}`, ""},
	} {
		home := t.TempDir()
		t.Setenv("LIVEAGENT_HOME", home)
		p := filepath.Join(home, "config.json")
		if tc.config != "" {
			if err := os.WriteFile(p, []byte(tc.config), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if tc.backup != "" {
			if err := os.WriteFile(p+".bak", []byte(tc.backup), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(home, memorySwitchFileName), []byte(`{"enabled":false}`), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if cfg.Memory.IsEnabled() {
			t.Fatalf("%s: recovery turned memory back on", tc.name)
		}
		again, err := Load()
		if err != nil || again.Memory.IsEnabled() {
			t.Fatalf("%s: memory off lost after recovery save: err=%v", tc.name, err)
		}
	}
}
