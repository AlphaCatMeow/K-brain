package mcp

import (
	"path/filepath"
	"testing"
)

func TestComputerServersOwnedByBackend(t *testing.T) {
	for _, server := range []LiveServer{
		{ID: "cua-driver", Enabled: true, Command: "missing-fixture", Transport: "stdio"},
		{ID: "renamed", Enabled: true, Command: `C:\Programs\Cua\cua-driver.exe`, Transport: "stdio"},
	} {
		manager, err := NewLiveManager(t.Context(), filepath.Join(t.TempDir(), "mcp.json"), LiveSettings{Servers: []LiveServer{server}})
		if err != nil {
			t.Fatal(err)
		}
		if statuses := manager.Statuses(); len(statuses) != 1 || statuses[0].Status != "managed" {
			t.Fatalf("status: %+v", statuses)
		}
		if len(manager.ToolsFor(t.Context(), t.TempDir(), nil)) != 0 {
			t.Fatal("duplicate computer MCP tools exposed")
		}
		manager.Close()
	}
}
