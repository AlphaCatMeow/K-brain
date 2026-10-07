package backend

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/memory"
	"github.com/Stack-Cairn/K-brain/internal/memoryruntime"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type cronWiringClient struct{ scriptedClient }

func (c *cronWiringClient) Clone() ai.Client { return c }
func (c *cronWiringClient) Stream(_ context.Context, request ai.Request, text, think func(string), tool func(string, string, string)) (ai.Message, ai.Usage, error) {
	foundMemory := false
	for _, m := range request.Messages {
		if m.Role == "user" && strings.Contains(m.Content, "<kbrain-memory-runtime>") && strings.Contains(m.Content, "Memory Index") {
			foundMemory = true
		}
	}
	if !foundMemory {
		return ai.Message{}, ai.Usage{}, fmt.Errorf("scheduled prompt omitted memory injection")
	}
	if request.ReasoningEffort != "high" {
		return ai.Message{}, ai.Usage{}, fmt.Errorf("reasoning=%s", request.ReasoningEffort)
	}
	names := map[string]bool{}
	for _, d := range request.Tools {
		names[d.Function.Name] = true
	}
	for _, name := range []string{"CronTaskManager", "Read", "memory_manager", "mcp__scheduled__echo"} {
		if !names[name] {
			return ai.Message{}, ai.Usage{}, fmt.Errorf("missing runtime tool %s", name)
		}
	}
	for _, m := range request.Messages {
		if m.Role == "tool" && m.Name == "mcp__scheduled__echo" {
			if !strings.Contains(m.Content, "scheduled MCP result") {
				return ai.Message{}, ai.Usage{}, fmt.Errorf("MCP result=%s", m.Content)
			}
			text("wired answer")
			return ai.Message{Role: "assistant", Content: "wired answer", StopReason: ai.StopReasonStop}, ai.Usage{PromptTokens: 12, CompletionTokens: 3}, nil
		}
	}
	call := ai.ToolCall{ID: "scheduled-tool", Type: "function"}
	call.Function.Name = "mcp__scheduled__echo"
	call.Function.Arguments = "{}"
	return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{call}, StopReason: ai.StopReasonToolUse}, ai.Usage{}, nil
}

func TestCronCanonicalMCPMemoryHooksReasoning(t *testing.T) {
	root := t.TempDir()
	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ms, err := memory.OpenStore(filepath.Join(root, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	sdk := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "cron-fixture"}, nil)
	sdkmcp.AddTool(sdk, &sdkmcp.Tool{Name: "echo", Description: "scheduled echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdkmcp.CallToolRequest, struct{}) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "scheduled MCP result"}}}, nil, nil
	})
	upstream := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return sdk }, &sdkmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer upstream.Close()
	live, err := mcp.NewLiveManager(context.Background(), filepath.Join(root, "mcp.json"), mcp.LiveSettings{Servers: []mcp.LiveServer{{ID: "scheduled", Enabled: true, Transport: "http", URL: upstream.URL, Policy: map[string]string{"echo": "allow"}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	deadline := time.Now().Add(5 * time.Second)
	for len(live.ToolsFor(context.Background(), root, nil)) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("MCP not ready: %+v", live.Statuses())
		}
		time.Sleep(10 * time.Millisecond)
	}
	hooks := make(chan [3]string, 4)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hooks <- [3]string{r.Header.Get("X-LiveAgent-Hook-Event"), r.Header.Get("X-LiveAgent-Conversation-Id"), r.Header.Get("X-LiveAgent-Run-Id")}
		w.WriteHeader(204)
	}))
	defer hook.Close()
	service, err := New(Options{Store: store, EventDir: filepath.Join(root, "events"), MemoryRoot: filepath.Join(root, "memory"), DefaultCWD: root, MCP: live, Factory: func(_ context.Context, _ string, m protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(&cronWiringClient{}, m.Model, 4096, "system"), nil
	}, MemoryRuntimeFactory: func(context.Context, string, protocol.ModelRef) (*memoryruntime.Runtime, error) {
		return memoryruntime.New(memoryruntime.Config{Store: ms, ResolveModel: func(context.Context, string) (ai.Client, string, error) { return &cronTestClient{}, "model", nil }})
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	server := httptest.NewServer(service)
	defer server.Close()
	var hs HooksSnapshot
	if code := doCronJSON(t, server.URL+"/v1/hooks", http.MethodGet, nil, &hs); code != 200 {
		t.Fatal(code)
	}
	change := HooksApplyInput{BaseRevision: hs.Revision, Ops: []HookOperation{{Op: "create", Item: map[string]any{"id": "scheduled-hook", "name": "scheduled-hook", "event": "agent_end", "enabled": true, "type": "http", "requests": []any{map[string]any{"id": "notify", "url": hook.URL, "method": "POST"}}}}}}
	if code := doCronJSON(t, server.URL+"/v1/hooks", http.MethodPut, change, nil); code != 200 {
		t.Fatal(code)
	}
	createCronPromptForTest(t, server.URL, root, "wired")
	if code := doCronJSON(t, server.URL+"/v1/cron", http.MethodPut, CronApplyInput{BaseRevision: 1, Ops: []CronOperation{{Op: "update", ID: "wired", Patch: map[string]any{"reasoning": "high"}}}}, nil); code != 200 {
		t.Fatal(code)
	}
	if code := doCronJSON(t, server.URL+"/v1/cron/wired/run-now", http.MethodPost, nil, nil); code != 202 {
		t.Fatal(code)
	}
	runs := waitCronRuns(t, server.URL, "wired", func(r []CronRunRecord) bool { return len(r) == 1 && r[0].State == "done" })
	run := runs[0]
	if !run.Success || run.Output != "wired answer" {
		t.Fatalf("run=%+v", run)
	}
	select {
	case h := <-hooks:
		if h != [3]string{"agent_end", run.SessionID, run.RunID} {
			t.Fatalf("hook=%+v run=%+v", h, run)
		}
	default:
		t.Fatal("canonical lifecycle hook missing")
	}
	response, err := http.Get(server.URL + "/v1/sessions/" + run.SessionID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, e := range readSSE(t, response) {
		counts[e.Type]++
	}
	for _, kind := range []string{protocol.EventToolCall, protocol.EventToolResult, protocol.EventUsage, protocol.EventRunCompleted} {
		if counts[kind] == 0 {
			t.Fatalf("missing event %s: %+v", kind, counts)
		}
	}
}
