package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

type cronCreationClient struct{ scriptedClient }

func (c *cronCreationClient) Clone() ai.Client { return c }
func (c *cronCreationClient) Stream(_ context.Context, request ai.Request, text, think func(string), tool func(string, string, string)) (ai.Message, ai.Usage, error) {
	for _, message := range request.Messages {
		if message.Role == "tool" && message.Name == "CronTaskManager" {
			text("creation handled")
			return ai.Message{Role: "assistant", Content: "creation handled", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
		}
	}
	found := false
	for _, definition := range request.Tools {
		if definition.Function.Name == "CronTaskManager" {
			found = true
		}
	}
	if !found {
		return ai.Message{}, ai.Usage{}, fmt.Errorf("CronTaskManager was not registered for the HTTP run")
	}
	call := ai.ToolCall{ID: "create-scheduled-task", Type: "function"}
	call.Function.Name = "CronTaskManager"
	call.Function.Arguments = `{"action":"create","name":"model-created","cron":"0 0 0 1 1 0","type":"prompt","prompt":"scheduled continuation","enabled":false}`
	return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{call}, StopReason: ai.StopReasonToolUse}, ai.Usage{}, nil
}

func TestCronToolCreationThroughCanonicalHTTPRun(t *testing.T) {
	for _, policy := range []string{"auto", "deny"} {
		t.Run(policy, func(t *testing.T) {
			root := t.TempDir()
			store, err := session.Open(filepath.Join(root, "sessions"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			service, err := New(Options{Store: store, EventDir: filepath.Join(root, "events"), DefaultCWD: root,
				Factory: func(_ context.Context, cwd string, selected protocol.ModelRef) (*agent.Agent, error) {
					a := agent.New(&cronCreationClient{}, selected.Model, 4096, "system")
					a.WorkingDir, a.Provider = cwd, selected.Provider
					return a, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()
			server := httptest.NewServer(service)
			defer server.Close()
			sess := createTestSession(t, server.URL)
			var accepted protocol.RunAccepted
			response := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{
				ClientRequestID: "create-task", Prompt: "schedule a prompt", Options: &protocol.RunOptions{ApprovalPolicy: policy},
			}, &accepted)
			response.Body.Close()
			if response.StatusCode != http.StatusAccepted {
				t.Fatalf("acceptance=%d", response.StatusCode)
			}
			events := waitForRun(t, server.URL, sess.ID, 0)
			var result *protocol.ToolResultEvent
			for _, event := range events {
				if event.Type == protocol.EventToolResult {
					var current protocol.ToolResultEvent
					if err := json.Unmarshal(event.Payload, &current); err != nil {
						t.Fatal(err)
					}
					if current.ToolResult.Name == "CronTaskManager" {
						result = &current
					}
				}
			}
			if result == nil {
				t.Fatal("missing canonical CronTaskManager tool result")
			}
			if result.ToolResult.Failed != (policy == "deny") {
				t.Fatalf("tool result=%+v", result)
			}
			var snapshot CronSnapshot
			if code := doCronJSON(t, server.URL+"/v1/cron", http.MethodGet, nil, &snapshot); code != 200 {
				t.Fatal(code)
			}
			if policy == "deny" {
				if len(snapshot.Tasks) != 0 {
					t.Fatal("denied tool created a task")
				}
				return
			}
			if len(snapshot.Tasks) != 1 {
				t.Fatalf("tasks=%+v", snapshot.Tasks)
			}
			task := snapshot.Tasks[0]
			if task.Name != "model-created" || task.Enabled || task.Workdir != sess.CWD || task.Reasoning != "medium" || task.SelectedModel == nil || task.SelectedModel.CustomProviderID != sess.Model.Provider || task.SelectedModel.Model != sess.Model.Model {
				t.Fatalf("task did not inherit session defaults: %+v", task)
			}
			reloaded, err := newCronStore(store)
			if err != nil {
				t.Fatal(err)
			}
			if tasks := reloaded.snapshot().Tasks; len(tasks) != 1 || tasks[0].ID != task.ID {
				t.Fatalf("task not persisted: %+v", tasks)
			}
		})
	}
}
