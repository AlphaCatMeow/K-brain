package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Review (25a3788), passing table: Load's empty-config recovery branches with a legacy
// "memory" value in config.json, a legacy "memory" value inside .bak, and each prior switch
// file state. The switch file wins when it exists; otherwise config.json's legacy value wins
// over the backup's; otherwise the backup's own value counts. After the recovery save
// config.json carries no "memory" key, the switch file matches the result, and a second
// Load agrees. Language and MCP servers merge as before.
func TestReviewMemorySwitchRecoveryPrecedenceTable(t *testing.T) {
	const backupHealthy = `{"providers":{"x":{"api":"openai-completions","models":[{"id":"m"}]}}`
	legacyConfig := map[string]string{
		"config none": ``,
		"config off":  `,"memory":{"enabled":false}`,
		"config on":   `,"memory":{"enabled":true}`,
	}
	legacyBackup := map[string]string{
		"backup none": ``,
		"backup off":  `,"memory":{"enabled":false}`,
		"backup on":   `,"memory":{"enabled":true}`,
		"no backup":   "-",
	}
	for _, sw := range reviewSwitchStates {
		for cName, cMem := range legacyConfig {
			for bName, bMem := range legacyBackup {
				name := sw.name + "/" + cName + "/" + bName
				home := t.TempDir()
				t.Setenv("LIVEAGENT_HOME", home)
				p := filepath.Join(home, "config.json")
				side := memorySwitchPath(p)
				conf := `{"language":"zh","mcp":{"s":{"command":["x"]}}` + cMem + `}`
				if err := os.WriteFile(p, []byte(conf), 0o600); err != nil {
					t.Fatal(err)
				}
				if bMem != "-" {
					if err := os.WriteFile(p+".bak", []byte(backupHealthy+bMem+`}`), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if sw.bytes != nil {
					if err := os.WriteFile(side, sw.bytes, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				wantOn := true
				switch {
				case sw.bytes != nil:
					wantOn = sw.loadOn
				case cMem != "":
					wantOn = !strings.Contains(cMem, "false")
				case bMem != "-" && bMem != "":
					wantOn = !strings.Contains(bMem, "false")
				}
				cfg, err := Load()
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if cfg.Memory.IsEnabled() != wantOn {
					t.Fatalf("%s: recovery on=%v want %v", name, cfg.Memory.IsEnabled(), wantOn)
				}
				if cfg.Language != "zh" || len(cfg.MCPServers) != 1 {
					t.Fatalf("%s: language=%q mcp=%d, want zh/1", name, cfg.Language, len(cfg.MCPServers))
				}
				if _, fromBackup := cfg.Providers["x"]; fromBackup != (bMem != "-") {
					t.Fatalf("%s: restored from backup=%v, want %v", name, fromBackup, bMem != "-")
				}
				data, _ := os.ReadFile(p)
				if strings.Contains(string(data), `"memory"`) {
					t.Fatalf("%s: config.json kept the memory key:\n%s", name, data)
				}
				_, statErr := os.Stat(side)
				if wantOn == (statErr == nil) {
					t.Fatalf("%s: on=%v but switch file exists=%v", name, wantOn, statErr == nil)
				}
				again, err := Load()
				if err != nil || again.Memory.IsEnabled() != wantOn {
					t.Fatalf("%s: second Load want on=%v (%v)", name, wantOn, err)
				}
				viaFile, err := LoadFile(p)
				if err != nil || viaFile.Memory.IsEnabled() != wantOn {
					t.Fatalf("%s: LoadFile want on=%v (%v)", name, wantOn, err)
				}
			}
		}
	}
}

// Review (25a3788), passing: a healthy config.json (providers present) never enters the
// recovery branches, so a legacy value there keeps the old path: LoadFile/Load honor it and
// the switch file still wins over it.
func TestReviewMemorySwitchHealthyLoadBranchUnchanged(t *testing.T) {
	for _, sw := range reviewSwitchStates {
		home := t.TempDir()
		t.Setenv("LIVEAGENT_HOME", home)
		p := filepath.Join(home, "config.json")
		legacy := `{"providers":{"x":{"api":"openai-completions","models":[{"id":"m"}]}},"memory":{"enabled":false}}`
		if err := os.WriteFile(p, []byte(legacy), 0o600); err != nil {
			t.Fatal(err)
		}
		if sw.bytes != nil {
			if err := os.WriteFile(memorySwitchPath(p), sw.bytes, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		want := false
		if sw.bytes != nil {
			want = sw.loadOn
		}
		cfg, err := Load()
		if err != nil || cfg.Memory.IsEnabled() != want {
			t.Fatalf("%s: Load on=%v want %v (%v)", sw.name, cfg != nil && cfg.Memory.IsEnabled(), want, err)
		}
		data, _ := os.ReadFile(p)
		if string(data) != legacy {
			t.Fatalf("%s: healthy Load rewrote config.json:\n%s", sw.name, data)
		}
	}
}
