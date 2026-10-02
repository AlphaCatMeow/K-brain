package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

type runControlsClient struct {
	scriptedClient
	lock     sync.Mutex
	requests []ai.Request
	tool     string
	args     string
	plan     bool
}

func (c *runControlsClient) Stream(_ context.Context, req ai.Request, text, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.lock.Lock()
	c.requests = append(c.requests, req)
	first := len(c.requests) == 1
	c.lock.Unlock()
	if first {
		call := ai.ToolCall{ID: "controls-call", Type: "function"}
		call.Function.Name, call.Function.Arguments = c.tool, c.args
		calls := []ai.ToolCall{call}
		if c.plan {
			exit := ai.ToolCall{ID: "plan-call", Type: "function"}
			exit.Function.Name, exit.Function.Arguments = "ExitPlanMode", `{"plan":"Review before implementation"}`
			calls = append(calls, exit)
		}
		return ai.Message{Role: "assistant", ToolCalls: calls}, ai.Usage{}, nil
	}
	if text != nil {
		text("done")
	}
	return ai.Message{Role: "assistant", Content: "done", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
}

func TestRunControlsHTTPExecutePoliciesRootsReasoningAndPlan(t *testing.T) {
	for _, tc := range []struct {
		name, tool, policy, reasoning, access string
		plan, failed, permission              bool
	}{
		{"external-read", "Read", "allow", "minimal", "read", false, false, false},
		{"external-write", "Write", "allow", "max", "write", false, false, false},
		{"read-only-write", "Write", "allow", "minimal", "read", false, true, false},
		{"ask-write", "Write", "ask", "max", "write", false, true, true},
		{"deny-write", "Write", "deny", "minimal", "write", false, true, false},
		{"plan-write", "Write", "allow", "max", "write", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			external := t.TempDir()
			target := filepath.Join(external, "target.txt")
			if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			args, _ := json.Marshal(map[string]string{"path": target, "content": "changed"})
			if tc.tool == "Read" {
				args, _ = json.Marshal(map[string]string{"path": target})
			}
			client := &runControlsClient{tool: tc.tool, args: string(args), plan: tc.plan}
			store, err := session.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			_, server := newToolHistoryServer(t, store, t.TempDir(), client)
			sess := createTestSession(t, server.URL)
			options := protocol.RunOptions{Mode: "agent", Reasoning: tc.reasoning, Search: "enabled", ApprovalPolicy: "auto", PlanModeEnabled: tc.plan, WorkspaceRoots: []protocol.WorkspaceRoot{{Path: sess.CWD, Access: "write"}, {Path: external, Access: tc.access}}, Tools: &protocol.ToolSelection{Policies: map[string]string{tc.tool: tc.policy}}}
			var accepted protocol.RunAccepted
			resp := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "controls", Prompt: "inspect", Options: &options}, &accepted)
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("accept status=%d", resp.StatusCode)
			}
			events := readToolHistoryRun(t, server.URL, sess.ID, accepted.RunID, "reject")
			if !hasToolHistoryEvent(events, protocol.EventRunCompleted) {
				t.Fatalf("events=%+v", events)
			}
			if hasToolHistoryEvent(events, protocol.EventPermissionRequest) != tc.permission {
				t.Fatalf("permission events=%+v", events)
			}
			var found bool
			for _, event := range events {
				if event.Type == protocol.EventToolResult {
					var result protocol.ToolResultEvent
					_ = json.Unmarshal(event.Payload, &result)
					if result.ToolResult.Name == tc.tool {
						found = true
						if result.ToolResult.Failed != tc.failed {
							t.Fatalf("result=%+v", result)
						}
					}
				}
			}
			if !found {
				t.Fatal("missing actual tool result")
			}
			data, _ := os.ReadFile(target)
			if tc.tool == "Write" && !tc.failed {
				if string(data) != "changed" {
					t.Fatalf("write=%q", data)
				}
			} else if string(data) != "original" {
				t.Fatalf("unauthorized mutation=%q", data)
			}
			client.lock.Lock()
			defer client.lock.Unlock()
			if !client.requests[0].NativeWebSearch {
				t.Fatal("search was not applied")
			}
			if client.requests[0].ReasoningEffort != tc.reasoning {
				t.Fatalf("reasoning=%q", client.requests[0].ReasoningEffort)
			}
			if tc.plan && len(client.requests) != 1 {
				t.Fatalf("plan continued after submission: %d calls", len(client.requests))
			}
			if tc.plan {
				for _, tool := range client.requests[0].Tools {
					if strings.EqualFold(tool.Function.Name, "write") {
						t.Fatal("plan exposed write")
					}
				}
			}
		})
	}
}

var _ ai.Client = (*runControlsClient)(nil)

func TestRunControlsHTTPChatBlocksHallucinatedTool(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "blocked.txt")
	args, _ := json.Marshal(map[string]string{"path": target, "content": "bad"})
	client := &runControlsClient{tool: "Write", args: string(args)}
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newToolHistoryServer(t, store, t.TempDir(), client)
	sess := createTestSession(t, server.URL)
	var accepted protocol.RunAccepted
	response := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "chat-controls", Prompt: "chat", Options: &protocol.RunOptions{Mode: "chat", PlanModeEnabled: true, ApprovalPolicy: "auto"}}, &accepted)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d", response.StatusCode)
	}
	events := readToolHistoryRun(t, server.URL, sess.ID, accepted.RunID, "reject")
	if !toolHistoryToolResult(t, events).Failed {
		t.Fatal("chat executed tool")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("chat mutated filesystem")
	}
	client.lock.Lock()
	defer client.lock.Unlock()
	if len(client.requests[0].Tools) != 0 {
		t.Fatal("chat exposed tools")
	}
}
