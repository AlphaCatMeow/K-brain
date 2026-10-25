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
		// No file means on; a legacy "memory" value differs and moves on the next save.
		c.memoryDisk = memoryDiskState{known: true, on: true}
		return
	}
	m, ok := decodeMemorySwitch(data, err)
	if !ok {
		logf("config.load", "unreadable memory switch file, keeping memory off")
	}
	c.Memory = m
	c.memoryDisk = memoryDiskState{known: true, on: m.IsEnabled(), file: true}
}

// AdoptMemory takes the switch from a freshly loaded config, with the disk state that load
// observed, so this config shows it and a later Save writes it only when the disk needs it
// (a legacy config.json value still moves into the switch file).
func (c *Config) AdoptMemory(from *Config) {
	c.Memory = from.Memory
	c.memoryDisk = from.memoryDisk
}

// memoryDiskState is what this config last saw of the switch file.
type memoryDiskState struct {
	known bool // false for a config that was never loaded or saved
	on    bool
	file  bool
}

// memoryDiskFor is the state a successful save of m leaves: a file only while off.
func memoryDiskFor(m MemoryConfig) memoryDiskState {
	return memoryDiskState{known: true, on: m.IsEnabled(), file: !m.IsEnabled()}
}

// needsWrite reports whether saving m must touch the switch file: always when the disk
// state is unknown, when m differs from it, and to clear a leftover file that says on.
func (d memoryDiskState) needsWrite(m MemoryConfig) bool {
	return !d.known || d.on != m.IsEnabled() || m.IsEnabled() && d.file
}

// decodeMemorySwitch reads a switch file that exists. Only an off switch is ever written,
// so a file that cannot be read (locked by another process, damaged) still means off.
func decodeMemorySwitch(data []byte, readErr error) (MemoryConfig, bool) {
	var m MemoryConfig
	if readErr == nil && json.Unmarshal(data, &m) == nil && m.Enabled != nil {
		return m, true
	}
	off := false
	return MemoryConfig{Enabled: &off}, false
}

// saveMemorySwitch writes the switch file for off and removes it for on, leaving it alone
// when the value is unchanged so a file held by another process cannot block unrelated
// saves. The returned restore puts the previous state back when the config.json write
// that follows fails.
func saveMemorySwitch(configPath string, m MemoryConfig) (restore func(), err error) {
	p := memorySwitchPath(configPath)
	previous, readErr := os.ReadFile(p)
	existed := !os.IsNotExist(readErr)
	wasOff := false
	if existed {
		current, _ := decodeMemorySwitch(previous, readErr)
		wasOff = !current.IsEnabled()
	}
	off, err := json.MarshalIndent(MemoryConfig{Enabled: new(bool)}, "", "  ")
	if err != nil {
		return nil, err
	}
	unchanged := func() {}
	if m.IsEnabled() && !existed || !m.IsEnabled() && wasOff {
		return unchanged, nil
	}
	restore = func() {
		switch {
		case !existed:
			_ = os.Remove(p)
		case readErr == nil:
			_ = writeMemorySwitch(p, previous)
		default:
			// The unreadable file meant off; put that state back.
			_ = writeMemorySwitch(p, off)
		}
	}
	if m.IsEnabled() {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		return restore, nil
	}
	if err := writeMemorySwitch(p, off); err != nil {
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
