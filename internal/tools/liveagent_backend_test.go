package tools_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/backend"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestLiveAgentBackendHTTPSkillsManagerRegistrationAndCall(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", home)
	t.Setenv("K_BRAIN_HOME", home)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		var hasSkillsManager bool
		for _, tool := range req.Tools {
			if tool.Function.Name == "SkillsManager" {
				hasSkillsManager = true
				break
			}
		}
		if !hasSkillsManager {
			t.Errorf("production backend session omitted SkillsManager from tool enumeration")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) == 1 {
			delta := map[string]any{
				"choices": []any{map[string]any{
					"delta": map[string]any{
						"tool_calls": []any{map[string]any{
							"index": 0, "id": "skills-1", "type": "function",
							"function": map[string]any{"name": "SkillsManager", "arguments": `{"action":"list"}`},
						}},
					},
				}},
			}
			data, _ := json.Marshal(delta)
			fmt.Fprintf(w, "data: %s\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", data)
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	store, err := session.Open(filepath.Join(home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	srv, err := backend.New(backend.Options{Store: store, EventDir: filepath.Join(home, "events"), DefaultCWD: dir, Token: "secret", Factory: func(_ context.Context, cwd string, model protocol.ModelRef) (*agent.Agent, error) {
		a := agent.New(ai.New(upstream.URL, "fixture-key"), model.Model, 1024, "Use SkillsManager")
		a.WorkingDir = cwd
		return a, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	server := httptest.NewServer(srv)
	defer server.Close()
	client := &http.Client{Timeout: 15 * time.Second}
	post := func(path string, in, out any) {
		t.Helper()
		data, _ := json.Marshal(in)
		req, err := http.NewRequest(http.MethodPost, server.URL+path, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s: HTTP %d %s", path, resp.StatusCode, body)
		}
		if out != nil {
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				t.Fatal(err)
			}
		}
	}
	var created protocol.Session
	post("/v1/sessions", protocol.CreateSessionRequest{CWD: dir, Model: protocol.ModelRef{Provider: "fixture", Model: "fixture"}}, &created)
	var accepted protocol.RunAccepted
	post("/v1/sessions/"+created.ID+"/runs", protocol.PromptRequest{ConversationID: created.ID, ClientRequestID: "skills", Prompt: "list skills"}, &accepted)
	eventsReq, err := http.NewRequest(http.MethodGet, server.URL+"/v1/sessions/"+created.ID+"/events?after_seq=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	eventsReq.Header.Set("Authorization", "Bearer secret")
	resp, err := client.Do(eventsReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scan := bufio.NewScanner(resp.Body)
	var sawResult, terminal bool
	for scan.Scan() {
		line := scan.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event protocol.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == protocol.EventToolResult {
			var result protocol.ToolResultEvent
			if err := json.Unmarshal(event.Payload, &result); err != nil {
				t.Fatal(err)
			}
			if result.ToolResult.Name == "SkillsManager" && strings.Contains(result.ToolResult.Output, `"skills"`) {
				sawResult = true
			}
		}
		if event.Type == protocol.EventRunCompleted || event.Type == protocol.EventRunFailed || event.Type == protocol.EventRunCancelled {
			terminal = true
			break
		}
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	if !sawResult || !terminal {
		t.Fatalf("SkillsManager result=%t terminal=%t", sawResult, terminal)
	}
}

func TestLiveAgentBackendHTTPRegistrationAndSelection(t *testing.T) {
	for _, policy := range []string{"auto", "deny"} {
		t.Run(policy, func(t *testing.T) {
			dir := t.TempDir()
			home := t.TempDir()
			t.Setenv("LIVEAGENT_HOME", home)
			t.Setenv("K_BRAIN_HOME", home)
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req ai.Request
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					http.Error(w, "invalid JSON", 400)
					return
				}
				hasWrite, hasRead := false, false
				for _, tool := range req.Tools {
					hasWrite = hasWrite || tool.Function.Name == "Write"
					hasRead = hasRead || tool.Function.Name == "Read"
				}
				if !hasWrite || !hasRead {
					t.Errorf("per-tool allow must retain unspecified tools: Write=%v Read=%v", hasWrite, hasRead)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if calls.Add(1) == 1 {
					delta := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "write-1", "type": "function", "function": map[string]any{"name": "Write", "arguments": `{"path":"provider.txt","content":"from HTTP"}`}}}}}}}
					data, _ := json.Marshal(delta)
					fmt.Fprintf(w, "data: %s\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", data)
				} else {
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				}
			}))
			defer upstream.Close()
			store, err := session.Open(filepath.Join(home, "sessions"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			srv, err := backend.New(backend.Options{Store: store, EventDir: filepath.Join(home, "events"), MemoryRoot: filepath.Join(home, "memory"), DefaultCWD: dir, Factory: func(_ context.Context, cwd string, model protocol.ModelRef) (*agent.Agent, error) {
				a := agent.New(ai.New(upstream.URL, "fixture-key"), model.Model, 1024, "Use tools")
				a.WorkingDir = cwd
				return a, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer srv.Close()
			server := httptest.NewServer(srv)
			defer server.Close()
			client := &http.Client{Timeout: 15 * time.Second}
			post := func(path string, in, out any) {
				t.Helper()
				data, _ := json.Marshal(in)
				resp, err := client.Post(server.URL+path, "application/json", bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if resp.StatusCode >= 300 {
					body, _ := io.ReadAll(resp.Body)
					t.Fatalf("%s: HTTP %d %s", path, resp.StatusCode, body)
				}
				if out != nil {
					if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
						t.Fatal(err)
					}
				}
			}
			var created protocol.Session
			post("/v1/sessions", protocol.CreateSessionRequest{CWD: dir, Model: protocol.ModelRef{Provider: "fixture", Model: "fixture"}}, &created)
			var accepted protocol.RunAccepted
			post("/v1/sessions/"+created.ID+"/runs", protocol.PromptRequest{ConversationID: created.ID, ClientRequestID: "test", Prompt: "write", Options: &protocol.RunOptions{ApprovalPolicy: policy, Tools: &protocol.ToolSelection{Enabled: []string{"Write"}}}}, &accepted)
			resp, err := client.Get(server.URL + "/v1/sessions/" + created.ID + "/events?after_seq=0")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			scan := bufio.NewScanner(resp.Body)
			scan.Buffer(make([]byte, 4096), 1<<20)
			sawResult, terminal := false, false
			for scan.Scan() {
				line := scan.Text()
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var event protocol.Event
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
					t.Fatal(err)
				}
				if event.Type == protocol.EventToolResult {
					var result protocol.ToolResultEvent
					if err := json.Unmarshal(event.Payload, &result); err != nil {
						t.Fatal(err)
					}
					if result.ToolResult.Name == "Write" {
						sawResult = true
						if result.ToolResult.Failed != (policy == "deny") {
							t.Errorf("policy=%s result=%+v", policy, result)
						}
					}
				}
				if event.Type == protocol.EventRunCompleted || event.Type == protocol.EventRunFailed || event.Type == protocol.EventRunCancelled {
					terminal = true
					break
				}
			}
			if err := scan.Err(); err != nil {
				t.Fatal(err)
			}
			if !sawResult || !terminal {
				t.Fatalf("result=%t terminal=%t", sawResult, terminal)
			}
			data, err := os.ReadFile(filepath.Join(dir, "provider.txt"))
			if policy == "auto" {
				if err != nil || string(data) != "from HTTP" {
					t.Fatalf("write=%q %v", data, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("denied Write changed file: %q %v", data, err)
			}
		})
	}
}
