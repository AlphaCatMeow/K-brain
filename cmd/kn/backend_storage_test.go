package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBackendStoragePreparationDoesNotStartRuntime(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", filepath.Join(t.TempDir(), "unused"))
	// This MCP command must never run during storage preparation.
	config := []byte(`{"mcp":{"fixture":{"command":["missing-command-must-not-execute"]}},"computer":{"command":["missing-cua-driver","mcp"]}}`)
	if err := os.WriteFile(filepath.Join(root, "config.json"), config, 0600); err != nil {
		t.Fatal(err)
	}
	if err := backendCLI([]string{"-data-dir", root, "-prepare-storage"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sessions", "memory"} {
		if info, err := os.Stat(filepath.Join(root, name)); err != nil || !info.IsDir() {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "config.json.live-mcp.json")); !os.IsNotExist(err) {
		t.Fatalf("started MCP manager: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sessions", "backend-events")); !os.IsNotExist(err) {
		t.Fatalf("started backend runtime: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil || string(got) != string(config) {
		t.Fatal("preparation rewrote user configuration")
	}
}
