package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestMCPStdioFixtureProcess(t *testing.T) {
	if os.Getenv("KBRAIN_MCP_STDIO_FIXTURE") != "1" {
		return
	}
	if err := Serve(context.Background(), "fixture"); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestLiveManagerWorkspaceAndSelectionFiltering(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	manager, err := NewLiveManager(context.Background(), filepath.Join(t.TempDir(), "settings.json"), LiveSettings{Servers: []LiveServer{{ID: "inside", Enabled: true, Transport: "stdio", Command: "true", WorkspaceRoots: []string{root}}, {ID: "outside", Enabled: true, Transport: "stdio", Command: "true", WorkspaceRoots: []string{outside}}}})
	if err != nil {
		t.Fatal(err)
	}
	manager.Close()
	if len(manager.ToolsFor(context.Background(), root, []string{"inside"})) != 0 {
		t.Fatal("unconnected fixture should not expose tools")
	}
}

func TestLiveManagerStdioAndHTTPReload(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stdioPath := filepath.Join(t.TempDir(), "stdio.json")
	stdioLive, err := NewLiveManager(context.Background(), stdioPath, LiveSettings{Servers: []LiveServer{{ID: "stdio", Enabled: true, Transport: "stdio", Command: bin, Args: []string{"-test.run=^TestMCPStdioFixtureProcess$"}, Env: map[string]string{"KBRAIN_MCP_STDIO_FIXTURE": "1"}, Cwd: repo}}})
	if err != nil {
		t.Fatal(err)
	}
	defer stdioLive.Close()
	waitLiveStatus(t, stdioLive, "stdio", "ready")
	stdioTools := stdioLive.ToolsFor(context.Background(), repo, nil)
	if len(stdioTools) < 4 {
		t.Fatalf("stdio fixture tools = %d, want at least 4", len(stdioTools))
	}
	stdioRead := tools.Execute(context.Background(), stdioTools, ToolName("stdio", "read"), json.RawMessage(`{"path":"internal/mcp/live.go","limit":1}`))
	if !strings.Contains(stdioRead, "package mcp") {
		t.Fatalf("stdio fixture call = %q", stdioRead)
	}

	httpServer := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "http-fixture"}, nil)
	sdkmcp.AddTool(httpServer, &sdkmcp.Tool{Name: "http_echo", Description: "echo over HTTP", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdkmcp.CallToolRequest, struct{}) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "http-ok"}}}, nil, nil
	})
	httpFixture := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return httpServer }, &sdkmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer httpFixture.Close()

	path := filepath.Join(t.TempDir(), "mcp.json")
	settings := LiveSettings{Servers: []LiveServer{{ID: "remote", Enabled: true, Transport: "http", URL: httpFixture.URL, Policy: map[string]string{"http_echo": "allow"}}}}
	live, err := NewLiveManager(context.Background(), path, settings)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	live.SetSave(func(next LiveSettings) error { return saveLiveSettings(path, next) })
	waitLiveStatus(t, live, "remote", "ready")
	result := toolsExecute(t, live.ToolsFor(context.Background(), t.TempDir(), nil), "mcp__remote__http_echo", nil)
	if result != "http-ok" {
		t.Fatalf("HTTP fixture call = %q", result)
	}

	updated := live.Settings()
	updated.Servers[0].Enabled = false
	if err := live.Replace(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	if got := len(live.ToolsFor(context.Background(), t.TempDir(), nil)); got != 0 {
		t.Fatalf("disabled reload exposed %d tools", got)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"enabled": false`) {
		t.Fatalf("reload did not persist disabled state: %v %s", err, data)
	}
}

func waitLiveStatus(t *testing.T, manager *LiveManager, name, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, status := range manager.Statuses() {
			if status.ID == name && status.Status == want {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("MCP server %s never reached %s: %+v", name, want, manager.Statuses())
}

func toolsExecute(t *testing.T, list []tools.Tool, name string, args json.RawMessage) string {
	t.Helper()
	return tools.ExecuteResult(context.Background(), list, name, args, false).Text
}
