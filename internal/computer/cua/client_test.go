package cua

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func fixture(t *testing.T, legacy bool, handler mcp.ToolHandler) *Client {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "cua-fixture", Version: "1"}, &mcp.ServerOptions{Instructions: "Original runtime instructions"})
	if legacy {
		server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				if method == "server/discover" {
					return nil, &jsonrpc.Error{Code: -32601, Message: "Method not found"}
				}
				return next(ctx, method, req)
			}
		})
	}
	for _, name := range []string{"get_window_state", "click", "run_actions", "end_session", "start_session", "list_sessions"} {
		server.AddTool(&mcp.Tool{Name: name, Description: "Original " + name, InputSchema: map[string]any{"type": "object", "properties": map[string]any{"pid": map[string]any{"type": "integer"}}}}, handler)
	}
	server.AddResource(&mcp.Resource{URI: "skill://cua-driver/SKILL.md", Name: "Cua guide"}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, Text: "Original bundled guide", MIMEType: "text/markdown"}}}, nil
	})
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	c := New(nil, t.TempDir())
	c.connect = func(context.Context) (mcp.Transport, error) { return ct, nil }
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = c.Close(ctx)
	})
	return c
}

func TestProtocolDiscoveryCallsAndLifecycle(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "modern", true: "legacy"}[legacy], func(t *testing.T) {
			var calls, cleanup atomic.Int32
			c := fixture(t, legacy, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				if req.Params.Name == "end_session" {
					if string(req.Params.Arguments) != "{}" {
						t.Errorf("not implicit cleanup: %s", req.Params.Arguments)
					}
					cleanup.Add(1)
					return textResult("ended"), nil
				}
				if string(req.Params.Arguments) != `{"pid":9007199254740993}` {
					t.Errorf("arguments lost precision or changed: %s", req.Params.Arguments)
				}
				calls.Add(1)
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "original"}}, StructuredContent: map[string]any{"count": calls.Load()}, IsError: calls.Load() == 2}, nil
			})
			if _, err := c.Call(t.Context(), "click", json.RawMessage(`{}`)); err == nil {
				t.Fatal("called before discovery")
			}
			out, err := c.Discover(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "Original runtime instructions") || strings.Contains(out, `"name":"end_session"`) {
				t.Fatal(out)
			}
			if _, err := c.Call(t.Context(), "click", json.RawMessage(`{}`)); err == nil {
				t.Fatal("called before describe")
			}
			for _, name := range []string{"end_session", "start_session", "list_sessions", "unknown"} {
				if _, err := c.Describe(t.Context(), name); err == nil {
					t.Fatalf("exposed %s", name)
				}
			}
			out, err = c.Describe(t.Context(), "click")
			if err != nil || !strings.Contains(out, `"inputSchema"`) {
				t.Fatalf("schema: %s %v", out, err)
			}
			for _, args := range []string{`null`, `[]`, `{} {}`, `{"session":"other"}`, `{"_session_id":"spoof"}`, `{"target":{"_transport_session_id":"spoof"}}`} {
				if _, err := c.Call(t.Context(), "click", json.RawMessage(args)); err == nil {
					t.Fatalf("accepted %s", args)
				}
			}
			for i := 0; i < 2; i++ {
				result, err := c.Call(t.Context(), "click", json.RawMessage(`{"pid":9007199254740993}`))
				if err != nil || result.IsError != (i == 1) || result.StructuredContent == nil {
					t.Fatalf("result: %+v %v", result, err)
				}
			}
			guide, err := c.Guide(t.Context(), "SKILL.md")
			if err != nil || !strings.Contains(guide, "Original bundled guide") {
				t.Fatalf("guide: %s %v", guide, err)
			}
			if _, err := c.Guide(t.Context(), "../../config.json"); err == nil {
				t.Fatal("arbitrary guide path allowed")
			}
			if err := c.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := c.Close(t.Context()); err != nil || cleanup.Load() != 1 {
				t.Fatalf("cleanup: %d %v", cleanup.Load(), err)
			}
			if _, err := c.Discover(t.Context()); err == nil {
				t.Fatal("closed client restarted")
			}
		})
	}
}

func TestCancellationPreventsReplay(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	c := fixture(t, false, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if req.Params.Name == "end_session" {
			return textResult("ended"), nil
		}
		calls.Add(1)
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Describe(t.Context(), "click"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.Call(ctx, "click", json.RawMessage(`{}`)); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation hung")
	}
	if _, err := c.Call(t.Context(), "click", json.RawMessage(`{}`)); err == nil {
		t.Fatal("replayed uncertain action")
	}
	if _, err := c.Discover(t.Context()); err == nil {
		t.Fatal("discovery cleared uncertainty")
	}
	if calls.Load() != 1 {
		t.Fatal("action replayed")
	}
}

func TestBatchAndDesktopOwnership(t *testing.T) {
	handler := func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return textResult("ok"), nil }
	a, b := fixture(t, false, handler), fixture(t, false, handler)
	if _, err := a.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Discover(t.Context()); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("second desktop owner: %v", err)
	}
	if _, err := a.Describe(t.Context(), "run_actions"); err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{`{"steps":[{"tool":"end_session","args":{}}]}`, `{"steps":[{"tool":"click","args":{}}]}`, `{"steps":[{"tool":"run_actions","args":{}}]}`} {
		if _, err := a.Call(t.Context(), "run_actions", json.RawMessage(args)); err == nil {
			t.Fatal("batch bypassed discovery/lifecycle restriction")
		}
	}
	if _, err := a.Describe(t.Context(), "click"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Call(t.Context(), "run_actions", json.RawMessage(`{"steps":[{"tool":"click","args":{}}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Discover(t.Context()); err != nil {
		t.Fatalf("ownership not released: %v", err)
	}
}

func TestCleanupErrorReleasesDesktop(t *testing.T) {
	c := fixture(t, false, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "cleanup failed"}}, IsError: true}, nil
	})
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(t.Context()); err == nil || !strings.Contains(err.Error(), "cleanup") {
		t.Fatalf("lost cleanup error: %v", err)
	}
	if desktopOwner.Load() != nil {
		t.Fatal("failed cleanup retained process ownership")
	}
}

func TestResolveExplicitCommand(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	argv, err := ResolveCommand([]string{exe, "mcp", "--socket", "a path with spaces", ""})
	if err != nil || len(argv) != 5 || argv[3] != "a path with spaces" || argv[4] != "" {
		t.Fatalf("argv changed: %q %v", argv, err)
	}
	if _, err := ResolveCommand([]string{""}); err == nil {
		t.Fatal("accepted empty executable")
	}
	if _, err := ResolveCommand([]string{"/missing/kbrain-cua-fixture"}); err == nil {
		t.Fatal("missing command fell back")
	}
}

func TestStdioFixture(t *testing.T) {
	if os.Getenv("KB_CUA_FIXTURE") != "1" {
		return
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "cua-fixture", Version: "1"}, nil)
	for _, name := range []string{"get_window_state", "end_session"} {
		s.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return textResult("ok"), nil })
	}
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestCommandTransport(t *testing.T) {
	t.Setenv("KB_CUA_FIXTURE", "1")
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := New([]string{exe, "-test.run=^TestStdioFixture$"}, t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	if _, err := c.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Describe(ctx, "get_window_state"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Call(ctx, "get_window_state", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	session := c.session
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = session.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("child did not exit")
	}
}

// This probe only reads protocol metadata and permissions, never a user's desktop.
func TestInstalledCuaReadOnly(t *testing.T) {
	if os.Getenv("KB_TEST_CUA_INSTALLED") != "1" {
		t.Skip("explicit installed-runtime probe only")
	}
	c := New([]string{"cua-driver", "mcp", "--direct"}, t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.Close(cleanup); err != nil {
			t.Error(err)
		}
	})
	out, err := c.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("discovered %d tools (%d bytes)", len(c.ordered), len(out))
	for _, name := range []string{"get_window_state", "click", "check_permissions"} {
		if _, err := c.Describe(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	guide, err := c.Guide(ctx, "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("bundled guide: %d bytes", len(guide))
	result, err := c.Call(ctx, "check_permissions", json.RawMessage(`{}`))
	if err != nil || result.IsError {
		t.Fatalf("permissions: %+v %v", result, err)
	}
	data, _ := json.Marshal(result)
	t.Logf("read-only permission status: %s", data)
}
