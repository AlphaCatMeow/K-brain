package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestTerminalHTTPRequiresCanonicalRunIdentity(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	factory := func(_ context.Context, _ string, selected protocol.ModelRef) (*agent.Agent, error) {
		client := &terminalHTTPModelClient{}
		ag := agent.New(client, selected.Model, 1024, "terminal")
		ag.ModelName, ag.Provider = selected.Model, selected.Provider
		return ag, nil
	}
	server, err := New(Options{Store: store, Factory: factory, EventDir: t.TempDir(), DefaultCWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/terminal/sessions", strings.NewReader(`{"command":"printf hi"}`))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "conversation_id and run_id") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

type terminalHTTPModelClient struct {
	entered chan context.Context
	release chan struct{}
}

func (*terminalHTTPModelClient) Models(context.Context) ([]ai.ModelInfo, error) { return nil, nil }
func (*terminalHTTPModelClient) Complete(context.Context, ai.Request) (string, ai.Usage, error) {
	return "", ai.Usage{}, nil
}
func (c *terminalHTTPModelClient) Clone() ai.Client   { return c }
func (c *terminalHTTPModelClient) SetCacheKey(string) {}
func (c *terminalHTTPModelClient) Endpoint() string   { return "terminal-http-test" }
func (c *terminalHTTPModelClient) Stream(ctx context.Context, _ ai.Request, _ func(string), _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	if c.entered != nil {
		c.entered <- ctx
		select {
		case <-c.release:
		case <-ctx.Done():
		}
	}
	return ai.Message{Role: "assistant", Content: "ok", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
}

func TestTerminalHTTPCanonicalProtocolAndAuthorization(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("K_BRAIN_SHELL", "powershell.exe")
	}
	for _, policy := range []string{"auto", "deny", "tool-deny", "plan", "chat", "read-only", "ask-cancel"} {
		t.Run(policy, func(t *testing.T) {
			store, err := session.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			client := &terminalHTTPModelClient{entered: make(chan context.Context, 1), release: make(chan struct{})}
			backend, err := New(Options{Store: store, DefaultCWD: t.TempDir(), EventDir: t.TempDir(), Factory: func(_ context.Context, _ string, selected protocol.ModelRef) (*agent.Agent, error) {
				return agent.New(client, "fixture", 1024, "terminal"), nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			host := httptest.NewServer(backend)
			defer host.Close()
			sess := createTestSession(t, host.URL)
			var accepted protocol.RunAccepted
			options := &protocol.RunOptions{ApprovalPolicy: "auto"}
			switch policy {
			case "ask-cancel":
				options.ApprovalPolicy = "ask"
			case "deny":
				options.ApprovalPolicy = "deny"
			case "tool-deny":
				options.Tools = &protocol.ToolSelection{Policies: map[string]string{"TerminalSession": "deny"}}
			case "plan":
				options.PlanModeEnabled = true
			case "chat":
				options.Mode = "chat"
			case "read-only":
				options.WorkspaceRoots = []protocol.WorkspaceRoot{{Path: backend.defaultCWD, Access: "read"}}
			}
			response := postJSON(t, http.DefaultClient, host.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ClientRequestID: "client-request-id", Prompt: "hold", Options: options}, &accepted)
			if response.StatusCode != 202 {
				t.Fatalf("run=%d", response.StatusCode)
			}
			var runCtx context.Context
			select {
			case runCtx = <-client.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("run did not start")
			}
			request := tools.TerminalRequest{Action: "start", ConversationID: sess.ID, RunID: accepted.RunID, Data: "printf bridge-output; sleep 30"}
			if runtime.GOOS == "windows" {
				request.Data = "Write-Output bridge-output; Start-Sleep -Seconds 30"
			}
			invoke := func(req tools.TerminalRequest) (int, tools.TerminalResponse) {
				t.Helper()
				raw, _ := json.Marshal(req)
				w := httptest.NewRecorder()
				backend.ServeHTTP(w, httptest.NewRequest("POST", "/v1/terminal", strings.NewReader(string(raw))))
				var out tools.TerminalResponse
				_ = json.Unmarshal(w.Body.Bytes(), &out)
				return w.Code, out
			}
			if policy == "ask-cancel" {
				raw, _ := json.Marshal(request)
				requestCtx, cancelRequest := context.WithCancel(context.Background())
				defer cancelRequest()
				done := make(chan int, 1)
				go func() {
					w := httptest.NewRecorder()
					backend.ServeHTTP(w, httptest.NewRequest("POST", "/v1/terminal", strings.NewReader(string(raw))).WithContext(requestCtx))
					done <- w.Code
				}()
				rt, _ := backend.loadRuntimeByID(sess.ID)
				for deadline := time.Now().Add(3 * time.Second); ; {
					rt.mu.Lock()
					pending := len(rt.permissions)
					rt.mu.Unlock()
					if pending > 0 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("approval request missing")
					}
					time.Sleep(10 * time.Millisecond)
				}
				cancelRequest()
				select {
				case code := <-done:
					if code != 403 {
						t.Fatalf("cancelled approval=%d", code)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("approval survived request cancellation")
				}
				rt.mu.Lock()
				pending := len(rt.permissions)
				rt.mu.Unlock()
				if pending != 0 {
					t.Fatal("approval waiter leaked")
				}
				close(client.release)
				return
			}
			code, started := invoke(request)
			if policy != "auto" {
				if code != 403 {
					t.Fatalf("deny status=%d", code)
				}
				close(client.release)
				return
			}
			if code != 200 || started.Session == nil {
				t.Fatalf("start=%d %+v", code, started)
			}
			request.Action, request.SessionID = "read", started.Session.ID
			for deadline := time.Now().Add(3 * time.Second); ; {
				code, out := invoke(request)
				if code != 200 {
					t.Fatalf("read=%d", code)
				}
				if strings.Contains(string(out.Output), "bridge-output") {
					if out.OutputEndOffset == 0 {
						t.Fatal("missing offsets")
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("missing output")
				}
				time.Sleep(10 * time.Millisecond)
			}
			rt, _ := backend.loadRuntimeByID(sess.ID)
			// The model tool reads the exact HTTP-owned session through the shared registry.
			for _, tool := range rt.agent.Tools {
				if tool.Def.Function.Name == "ReadTerminal" {
					output, e := tool.Run(runCtx, json.RawMessage(`{"session_id":"`+request.SessionID+`"}`))
					if e != nil || !strings.Contains(output, "bridge-output") {
						t.Fatalf("tool output=%q err=%v", output, e)
					}
				}
			}
			forged := request
			forged.RunID = "client-request-id"
			if code, _ := invoke(forged); code != 403 {
				t.Fatalf("forged run=%d", code)
			}
			outside := request
			outside.Action = "start"
			outside.CWD = t.TempDir()
			if code, _ := invoke(outside); code != 403 {
				t.Fatalf("outside cwd=%d", code)
			}
			// Multiple starts must preserve earlier buffers.
			second := request
			second.Action = "start"
			second.Data = "printf second"
			if runtime.GOOS == "windows" {
				second.Data = "Write-Output second"
			}
			if code, _ := invoke(second); code != 200 {
				t.Fatalf("second start=%d", code)
			}
			if code, _ := invoke(request); code != 200 {
				t.Fatalf("first session lost=%d", code)
			}
			close(client.release)
			select {
			case <-runCtx.Done():
			case <-time.After(3 * time.Second):
				t.Fatal("run context retained")
			}
			for deadline := time.Now().Add(3 * time.Second); ; {
				code, out := invoke(request)
				if code != 200 {
					t.Fatalf("completed buffer read=%d", code)
				}
				if !out.Session.Running {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("child survived run completion")
				}
				time.Sleep(10 * time.Millisecond)
			}
			request.Action = "start"
			if code, _ := invoke(request); code != 409 {
				t.Fatalf("completed start=%d", code)
			}
			backend.token = "fixture-token"
			if code, _ := invoke(request); code != 401 {
				t.Fatalf("missing bearer token=%d", code)
			}
		})
	}
}
