package backend

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// Releases without the memory switch reject an unknown "memory" key and refuse to start,
// so config.json must look as an older release wrote it whether memory is on or off.
func TestMemorySwitchOnLeavesConfigReadableByOlderReleases(t *testing.T) {
	f := newMemorySwitchFixture(t)
	topLevel := func() map[string]json.RawMessage {
		t.Helper()
		data, err := os.ReadFile(f.configPath)
		if err != nil {
			t.Fatal(err)
		}
		// The saved file starts with JSONC comment lines; the body is plain JSON.
		var body []string
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "//") {
				body = append(body, line)
			}
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal([]byte(strings.Join(body, "\n")), &keys); err != nil {
			t.Fatal(err)
		}
		return keys
	}

	f.setEnabled(t, true)
	if raw, ok := topLevel()["memory"]; ok {
		t.Fatalf("memory on wrote %s; older releases would refuse this config", raw)
	}

	f.setEnabled(t, false)
	if raw, ok := topLevel()["memory"]; ok {
		t.Fatalf("memory off wrote %s; older releases would refuse this config", raw)
	}
	if _, err := os.Stat(f.configPath + ".memory-switch.json"); err != nil {
		t.Fatalf("memory off left no switch file: %v", err)
	}
	cfg, err := config.LoadFile(f.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Memory.IsEnabled() {
		t.Fatal("memory off did not survive a reload")
	}

	f.setEnabled(t, true)
	if raw, ok := topLevel()["memory"]; ok {
		t.Fatalf("memory back on kept %s; older releases would refuse this config", raw)
	}
	if cfg, err = config.LoadFile(f.configPath); err != nil || !cfg.Memory.IsEnabled() {
		t.Fatalf("memory on after reload: enabled=%v err=%v", cfg != nil && cfg.Memory.IsEnabled(), err)
	}
}
