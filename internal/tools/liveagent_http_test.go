package tools_test

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

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestLiveAgentRealHTTPProviderFileLoop(t *testing.T) {
	dir := t.TempDir()
	calls := []struct{ name, args string }{
		{"Write", `{"path":"note.txt","content":"one\ntwo\n"}`},
		{"Read", `{"path":"note.txt","start_line":2,"limit":1}`},
		{"Edit", `{"path":"note.txt","old_string":"two","new_string":"second","expected_replacements":1}`},
		{"List", `{"depth":1}`},
		{"Glob", `{"pattern":"**/*.txt"}`},
		{"Grep", `{"pattern":"SECOND","ignore_case":true,"output_mode":"count"}`},
		{"Bash", `{"command":"printf shell-ok","timeout_ms":1000}`},
		{"Image", `{"base64":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6WQAAAABJRU5ErkJggg==","mimeType":"image/png"}`},
		{"Delete", `{"path":"note.txt"}`},
	}
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ai.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		seen := map[string]bool{}
		for _, tool := range request.Tools {
			seen[tool.Function.Name] = true
		}
		for _, name := range []string{"Read", "Image", "Write", "Edit", "Delete", "List", "Glob", "Grep", "Bash"} {
			if !seen[name] {
				t.Errorf("provider schema missing %s", name)
			}
		}
		n := int(requests.Add(1)) - 1
		if n > 0 {
			found := false
			for _, msg := range request.Messages {
				if msg.Role == "tool" && msg.ToolCallID == fmt.Sprintf("call-%d", n-1) {
					found = true
					if strings.HasPrefix(msg.Content, "Error:") {
						t.Errorf("%s failed: %s", calls[n-1].name, msg.Content)
					}
				}
			}
			if !found {
				t.Errorf("missing preceding result %d", n-1)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		var delta any
		reason := "stop"
		if n < len(calls) {
			reason = "tool_calls"
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call-%d", n), "type": "function", "function": map[string]any{"name": calls[n].name, "arguments": calls[n].args}}}}
		} else {
			delta = map[string]any{"content": "done"}
		}
		data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta}}})
		fmt.Fprintf(w, "data: %s\n\n", data)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":%q}]}\n\ndata: [DONE]\n\n", reason)
	}))
	defer upstream.Close()
	a := agent.New(ai.New(upstream.URL, "test-key"), "fixture", 1024, "Use the supplied file tools.")
	a.Tools = tools.LiveAgentCatalog()
	a.WorkingDir = dir
	a.MaxTurns = 16
	a.Vision = true
	ctx := tools.WithWorkspaceRoots(context.Background(), []tools.WorkspaceRoot{{Path: dir, Access: "write"}})
	var policyNames []string
	ctx = tools.WithGate(ctx, func(req tools.GateRequest) (tools.GateDecision, string) {
		policyNames = append(policyNames, req.Tool)
		return tools.GateAllowOnce, ""
	})
	if _, err := a.Turn(ctx, "Perform the file operations", agent.Events{}); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != int32(len(calls)+1) {
		t.Fatalf("HTTP requests=%d", requests.Load())
	}
	if _, err := os.Stat(filepath.Join(dir, "note.txt")); !os.IsNotExist(err) {
		t.Fatalf("Delete did not remove note: %v", err)
	}
	for _, name := range policyNames {
		if name == strings.ToLower(name) {
			t.Fatalf("lowercase policy alias leaked: %s", name)
		}
	}
}
