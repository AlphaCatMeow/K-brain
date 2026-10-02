package backend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

type managedProcessHTTPClient struct {
	mu        sync.Mutex
	calls     int
	processID string
	runCtx    context.Context
}

func (c *managedProcessHTTPClient) Models(context.Context) ([]ai.ModelInfo, error) { return nil, nil }
func (c *managedProcessHTTPClient) Complete(context.Context, ai.Request) (string, ai.Usage, error) {
	return "", ai.Usage{}, nil
}
func (c *managedProcessHTTPClient) Clone() ai.Client   { return c }
func (c *managedProcessHTTPClient) SetCacheKey(string) {}
func (c *managedProcessHTTPClient) Endpoint() string   { return "http://managed-process-fixture.invalid" }
func (c *managedProcessHTTPClient) Stream(ctx context.Context, request ai.Request, onText, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.runCtx = ctx
	for _, message := range request.Messages {
		if message.Role != "tool" {
			continue
		}
		for _, line := range strings.Split(message.Content, "\n") {
			if strings.HasPrefix(line, "process_id=mp-") {
				c.processID = strings.TrimPrefix(line, "process_id=")
			}
		}
	}
	call := ai.ToolCall{ID: "managed-start", Type: "function"}
	call.Function.Name = "ManagedProcess"
	call.Function.Arguments = `{"action":"start","command":"sleep 30"}`
	if c.calls == 2 {
		call.ID = "managed-stop"
		call.Function.Arguments = `{"action":"stop","process_id":"` + c.processID + `"}`
	}
	if c.calls > 2 {
		if onText != nil {
			onText("done")
		}
		return ai.Message{Role: "assistant", Content: "done", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
	}
	return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{call}}, ai.Usage{}, nil
}

func TestLiveAgentManagedProcessHTTPModelCall(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := &managedProcessHTTPClient{}
	factory := func(_ context.Context, _ string, selected protocol.ModelRef) (*agent.Agent, error) {
		ag := agent.New(client, selected.Model, 1024, "Use managed process tools.")
		ag.ModelName, ag.Provider = selected.Model, selected.Provider
		return ag, nil
	}
	backendServer, err := New(Options{Store: store, Factory: factory, EventDir: t.TempDir(), DefaultCWD: t.TempDir(), Models: []protocol.ModelRef{{Provider: "fixture", Model: "fixture-model"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer backendServer.Close()
	server := httptest.NewServer(backendServer)
	defer server.Close()
	sess := createTestSession(t, server.URL)
	var accepted protocol.RunAccepted
	response := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "managed-process-http", Prompt: "start and stop a managed process", Options: &protocol.RunOptions{ApprovalPolicy: "auto"}}, &accepted)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("run status=%d", response.StatusCode)
	}
	response, err = http.Get(server.URL + "/v1/sessions/" + sess.ID + "/events?after_seq=" + strconv.FormatInt(accepted.AcceptedSeq-1, 10))
	if err != nil {
		t.Fatal(err)
	}
	events := readSSE(t, response)
	if !hasEventType(events, protocol.EventToolResult) || !hasEventType(events, protocol.EventRunCompleted) {
		t.Fatalf("events=%+v", events)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.calls != 3 || !strings.HasPrefix(client.processID, "mp-") {
		t.Fatalf("model calls=%d process_id=%q", client.calls, client.processID)
	}
	select {
	case <-client.runCtx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("successful run retained its execution context")
	}
}

func hasEventType(events []protocol.Event, want string) bool {
	for _, event := range events {
		if event.Type == want {
			return true
		}
	}
	return false
}
