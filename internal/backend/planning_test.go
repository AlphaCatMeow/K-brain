package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/planning"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestPlanningHTTPToolsRestartAndAuth(t *testing.T) {
	root := t.TempDir()
	store, e := session.Open(filepath.Join(root, "sessions"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	options := Options{Store: store, Factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(&scriptedClient{}, "fixture", 100, ""), nil
	}, EventDir: filepath.Join(root, "events"), MemoryRoot: filepath.Join(root, "memory"), Token: "planning-token"}
	s, e := New(options)
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(s)
	call := func(action string, input any, out any) int {
		b, _ := json.Marshal(map[string]any{"action": action, "input": input})
		req, _ := http.NewRequest("POST", server.URL+"/v1/planning", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer planning-token")
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		if out != nil {
			if e = json.NewDecoder(resp.Body).Decode(out); e != nil {
				t.Fatal(e)
			}
		}
		return resp.StatusCode
	}
	var created planning.Result
	if status := call("mutate", map[string]any{"requestId": "http-create", "action": "todo.create", "data": map[string]any{"title": "shared task"}}, &created); status != 200 {
		t.Fatal(status, created)
	}
	var snapshot planning.Snapshot
	if call("query", map[string]any{}, &snapshot) != 200 || len(snapshot.Todos) != 1 {
		t.Fatal(snapshot)
	}
	ag := agent.New(&scriptedClient{}, "fixture", 100, "")
	s.attachPlanningTools(ag)
	names := map[string]bool{}
	for _, tool := range ag.Tools {
		names[tool.Def.Function.Name] = true
		if tool.Def.Function.Name == "PlanningQuery" {
			text, e := tool.Run(context.Background(), json.RawMessage(`{}`))
			if e != nil {
				t.Fatal(e)
			}
			var snap planning.Snapshot
			if json.Unmarshal([]byte(text), &snap) != nil || len(snap.Todos) != 1 {
				t.Fatal(text)
			}
		}
		if tool.Def.Function.Name == "PlanningMutate" {
			raw := json.RawMessage(`{"requestId":"tool-create","action":"todo.create","data":{"title":"tool task"}}`)
			denied := tools.WithGate(context.Background(), func(tools.GateRequest) (tools.GateDecision, string) {
				return tools.GateReject, "denied by test"
			})
			if _, err := tool.Run(denied, raw); err == nil {
				t.Fatal("denied tool mutation succeeded")
			}
			allowed := tools.WithGate(context.Background(), func(tools.GateRequest) (tools.GateDecision, string) {
				return tools.GateAllowOnce, ""
			})
			result, err := tool.Run(allowed, raw)
			if err != nil {
				t.Fatal(result, err)
			}
		}
	}
	if !names["PlanningQuery"] || !names["PlanningMutate"] {
		t.Fatal(names)
	}
	resp, e := http.Get(server.URL + "/v1/planning")
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("auth missing", resp.StatusCode)
	}
	server.Close()
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = New(options)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	snap, e := s.planning.Query(0, 0)
	if e != nil || len(snap.Todos) != 2 {
		t.Fatal("restart lost task", snap, e)
	}
}
func TestPlanningCronRangeAndBackendTasks(t *testing.T) {
	root := t.TempDir()
	store, e := session.Open(filepath.Join(root, "sessions"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	s, e := New(Options{Store: store, Factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(&scriptedClient{}, "fixture", 100, ""), nil
	}, MemoryRoot: filepath.Join(root, "memory")})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	_, e = s.cron.store.apply(CronApplyInput{BaseRevision: 0, Ops: []CronOperation{{Op: "create", Item: map[string]any{"id": "backend-only", "name": "daily", "cron": "0 0 12 * * *", "type": "bash", "script": "true", "enabled": true}}}})
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UnixMilli()
	raw, _ := json.Marshal(map[string]int64{"from": now, "to": now + 86400000})
	result, e := s.planningCron(raw)
	if e != nil {
		t.Fatal(e)
	}
	v := result.(map[string]any)
	if len(v["tasks"].([]map[string]any)) != 1 || len(v["occurrences"].([]map[string]any)) != 1 {
		t.Fatal(v)
	}
	if _, e = s.planningCron(json.RawMessage(`{"from":1,"to":1}`)); e == nil {
		t.Fatal("invalid range accepted")
	}
}
