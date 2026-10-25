package config

import (
	"path/filepath"
	"testing"
)

func TestExplicitSavePreservesExternallyChangedMemorySwitch(t *testing.T) {
	for _, originallyEnabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "on_to_off", false: "off_to_on"}[originallyEnabled], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			cfg := Default()
			cfg.Memory.Enabled = &originallyEnabled
			if err := cfg.SaveFile(path); err != nil {
				t.Fatal(err)
			}
			stale, err := LoadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			other, err := LoadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			next := !originallyEnabled
			other.Memory.Enabled = &next
			if err := other.SaveFile(path); err != nil {
				t.Fatal(err)
			}
			stale.Language = "zh"
			if err := stale.SaveFile(path); err != nil {
				t.Fatal(err)
			}
			reloaded, err := LoadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if reloaded.Memory.IsEnabled() != next {
				t.Fatalf("unrelated SaveFile reverted memory switch: got enabled=%v want %v", reloaded.Memory.IsEnabled(), next)
			}
		})
	}
}

func TestExplicitSaveTracksSwitchAndNewDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.json")
	cfg := Default()
	if err := cfg.SaveFile(path); err != nil {
		t.Fatal(err)
	}
	off := false
	cfg.Memory.Enabled = &off
	if err := cfg.SaveFile(path); err != nil {
		t.Fatal(err)
	}
	on := true
	cfg.Memory.Enabled = &on
	if err := cfg.SaveFile(path); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadFile(path)
	if err != nil || !reloaded.Memory.IsEnabled() {
		t.Fatalf("second toggle did not persist: cfg=%v err=%v", reloaded, err)
	}
	cfg.Memory.Enabled = &off
	if err := cfg.SaveFile(path); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "destination.json")
	if err := cfg.SaveFile(destination); err != nil {
		t.Fatal(err)
	}
	reloaded, err = LoadFile(destination)
	if err != nil || reloaded.Memory.IsEnabled() {
		t.Fatalf("new destination lost disabled switch: cfg=%v err=%v", reloaded, err)
	}
}
