package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Review probe: the default Save path (LIVEAGENT_HOME/config.json), a hand-written
// explicit "on", and the empty object all end up without a "memory" key.
func TestReviewMemorySwitchSaveLoadRoundTrips(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", home)
	p := filepath.Join(home, "config.json")
	read := func() string {
		t.Helper()
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	base := `{"defaultModel":"m","providers":{"x":{"api":"openai-completions","models":[{"id":"m"}]}}`
	for _, tc := range []struct {
		name, memory string
		enabled      bool
		kept         bool
	}{
		{"explicit on", `,"memory":{"enabled":true}}`, true, false},
		{"empty object", `,"memory":{}}`, true, false},
		{"null", `,"memory":null}`, true, false},
		{"off", `,"memory":{"enabled":false}}`, false, true},
		{"absent", `}`, true, false},
	} {
		if err := os.WriteFile(p, []byte(base+tc.memory), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load()
		if err != nil {
			t.Fatalf("%s: load: %v", tc.name, err)
		}
		if cfg.Memory.IsEnabled() != tc.enabled {
			t.Fatalf("%s: enabled=%v want %v", tc.name, cfg.Memory.IsEnabled(), tc.enabled)
		}
		if err := cfg.Save(); err != nil {
			t.Fatalf("%s: save: %v", tc.name, err)
		}
		if got := strings.Contains(read(), `"memory"`); got != tc.kept {
			t.Fatalf("%s: memory key kept=%v want %v:\n%s", tc.name, got, tc.kept, read())
		}
		again, err := Load()
		if err != nil || again.Memory.IsEnabled() != tc.enabled {
			t.Fatalf("%s: reload enabled=%v err=%v", tc.name, again != nil && again.Memory.IsEnabled(), err)
		}
	}
	// Snapshot copies must keep the off state (pointer shared, value copied).
	off := false
	cfg := &Config{Memory: MemoryConfig{Enabled: &off}}
	if cfg.Snapshot().Memory.IsEnabled() {
		t.Fatal("snapshot lost memory off")
	}
}
