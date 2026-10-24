package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// memorySwitchSuffix names the file beside each config file that keeps its memory switch,
// like the ".bak" and ".live-mcp.json" companions. Releases without the switch decode
// config.json with DisallowUnknownFields and refuse to start on a "memory" key; they never
// read this file, so rolling back runs with memory on and still starts. The file exists
// only while memory is off.
const memorySwitchSuffix = ".memory-switch.json"

func memorySwitchPath(configPath string) string { return configPath + memorySwitchSuffix }

// loadMemorySwitch applies the switch file. Without one, a "memory" value left in
// config.json by earlier builds of the switch still counts and moves here on the next save.
func (c *Config) loadMemorySwitch(configPath string) {
	data, err := os.ReadFile(memorySwitchPath(configPath))
	if os.IsNotExist(err) {
		return
	}
	var m MemoryConfig
	if err != nil || json.Unmarshal(data, &m) != nil || m.Enabled == nil {
		// Only an off switch is ever written, so a file that exists but cannot be read
		// (locked by another process, damaged) still means off.
		logf("config.load", "unreadable memory switch file, keeping memory off")
		off := false
		m.Enabled = &off
	}
	c.Memory = m
}

// saveMemorySwitch writes the switch file for off and removes it for on. The returned
// restore puts the previous file back when the config.json write that follows fails.
func saveMemorySwitch(configPath string, m MemoryConfig) (restore func(), err error) {
	p := memorySwitchPath(configPath)
	previous, readErr := os.ReadFile(p)
	restore = func() {
		if readErr == nil {
			_ = writeMemorySwitch(p, previous)
		} else if os.IsNotExist(readErr) {
			_ = os.Remove(p)
		}
	}
	if m.IsEnabled() {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		return restore, nil
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeMemorySwitch(p, data); err != nil {
		return nil, err
	}
	return restore, nil
}

func writeMemorySwitch(p string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(p), ".memory-switch-*")
	if err != nil {
		return err
	}
	return replaceConfigFile(f, p, data)
}
