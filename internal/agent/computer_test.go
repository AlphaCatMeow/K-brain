package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/tools"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestComputerBackendSelection(t *testing.T) {
	no := false
	for _, tc := range []struct {
		cfg  config.ComputerConfig
		want string
	}{
		{config.ComputerConfig{}, "installed Cua Driver"},
		{config.ComputerConfig{Backend: "legacy"}, "pseudocode"},
		{config.ComputerConfig{Enabled: &no}, ""},
	} {
		a := New(ai.New("http://unused", "unused"), "fixture", 100, "system", WithComputerConfig(tc.cfg))
		found := false
		for _, tool := range a.Tools {
			if tool.Def.Function.Name != "computer_exec" {
				continue
			}
			found = true
			if tc.want == "" || !strings.Contains(tool.Def.Function.Description, tc.want) {
				t.Fatalf("wrong computer backend: %s", tool.Def.Function.Description)
			}
		}
		if found != (tc.want != "") {
			t.Fatalf("tool visibility: %v", found)
		}
	}
}

func TestComputerAgentFixture(t *testing.T) {
	if os.Getenv("KB_AGENT_CUA_FIXTURE") != "1" {
		return
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "cua-agent-fixture", Version: "1"}, nil)
	for _, name := range []string{"get_window_state", "end_session"} {
		s.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if req.Params.Name == "end_session" {
				if err := os.WriteFile(os.Getenv("KB_AGENT_CUA_CLEANUP"), []byte("ended"), 0600); err != nil {
					return nil, err
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ended"}}}, nil
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fixture window state"}, &mcp.ImageContent{MIMEType: "image/png", Data: []byte("fixture image")}}}, nil
		})
	}
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestComputerAgentTurnEndToEnd(t *testing.T) {
	t.Setenv("KB_AGENT_CUA_FIXTURE", "1")
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	cleanup := filepath.Join(t.TempDir(), "cleanup")
	t.Setenv("KB_AGENT_CUA_CLEANUP", cleanup)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		n := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n <= 3 {
			args := []string{`{"action":"discover"}`, `{"action":"describe","tool":"get_window_state"}`, `{"action":"call","tool":"get_window_state","arguments":{}}`}[n-1]
			payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("computer-%d", n), "type": "function", "function": map[string]any{"name": "computer_exec", "arguments": args}}}}, "finish_reason": "tool_calls"}}})
			fmt.Fprintf(w, "data: %s\n\n", payload)
		} else {
			data, _ := json.Marshal(req.Messages)
			if !strings.Contains(string(data), "fixture window state") || !strings.Contains(string(data), "image_url") {
				t.Errorf("model lost desktop result/image: %s", data)
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"verified fixture\"},\"finish_reason\":\"stop\"}]}\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	a := New(ai.New(srv.URL, "fixture"), "fixture", 100, "fixture system", WithComputerConfig(config.ComputerConfig{Command: []string{exe, "-test.run=^TestComputerAgentFixture$"}}))
	a.Vision = true
	a.SetSessionID("fixture-conversation")
	answer, err := a.Turn(t.Context(), "inspect fixture", Events{})
	if err != nil || answer != "verified fixture" {
		t.Fatalf("turn: %q %v", answer, err)
	}
	if calls.Load() != 4 {
		t.Fatalf("model calls: %d", calls.Load())
	}
	if data, err := os.ReadFile(cleanup); err != nil || string(data) != "ended" {
		t.Fatalf("turn did not clean up: %s %v", data, err)
	}
}

func TestComputerTurnLazyAndMissingRuntime(t *testing.T) {
	a := New(ai.New("http://unused", "unused"), "fixture", 100, "system", WithComputerConfig(config.ComputerConfig{Command: []string{"/missing/kbrain-cua-test-fixture"}}))
	a.SetSessionID("conversation")
	ctx := tools.WithRunIdentity(t.Context(), tools.RunIdentity{ConversationID: "conversation", RunID: "run"})
	ctx, closeRuntime := a.computerTurn(ctx)
	result := tools.ExecuteResult(ctx, a.Tools, "computer_exec", []byte(`{"action":"discover"}`), true)
	if !result.Failed || !strings.Contains(result.Text, "computer.command") {
		t.Fatalf("missing runtime: %+v", result)
	}
	if err := closeRuntime(); err != nil {
		t.Fatal(err)
	}
	_, closeUnused := a.computerTurn(context.Background())
	if err := closeUnused(); err != nil {
		t.Fatalf("unused runtime launched: %v", err)
	}
}

func TestComputerSubagentAndPlanMode(t *testing.T) {
	a := New(ai.New("http://unused", "fixture"), "fixture", 100, "system", WithComputerConfig(config.ComputerConfig{Backend: "cua", Command: []string{"fixture", "mcp"}}))
	a.SetSessionID("parent")
	sub := a.newSub(SubModel{})
	if sub.ComputerConfig.Backend != "cua" || len(sub.ComputerConfig.Command) != 2 {
		t.Fatal("subagent lost computer configuration")
	}
	a.SetPlanMode(true)
	for _, tool := range a.AllTools() {
		if tool.Def.Function.Name == "computer_exec" {
			t.Fatal("plan mode exposed desktop mutation")
		}
	}
}
