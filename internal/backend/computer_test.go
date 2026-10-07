package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestComputerSettingsBackendOwnership(t *testing.T) {
	var saved *config.Config
	store := NewSettingsStore(&config.Config{}, func(cfg *config.Config) error { saved = cfg.Snapshot(); return nil })
	s := &Server{settings: store}
	update := httptest.NewRecorder()
	s.handleSettings(update, httptest.NewRequest(http.MethodPut, "/v1/settings", strings.NewReader(`{"computer":{"enabled":false,"backend":"cua","command":["/missing/cua-driver","mcp"],"approvalPolicy":"deny"}}`)))
	if update.Code != 200 || saved == nil || saved.Computer.Enabled == nil || *saved.Computer.Enabled || saved.Computer.ApprovalPolicy != "deny" {
		t.Fatalf("save: %d %s", update.Code, update.Body.String())
	}
	status := httptest.NewRecorder()
	s.handleComputer(status, httptest.NewRequest(http.MethodGet, "/v1/computer", nil))
	if status.Code != 200 || !strings.Contains(status.Body.String(), `"executionOwner":"kbrain"`) || !strings.Contains(status.Body.String(), `"installed":false`) {
		t.Fatalf("status: %s", status.Body.String())
	}
	for _, body := range []string{`{"computer":{"backend":"bad"}}`, `{"computer":{"approvalPolicy":"auto-approve"}}`, `{"computer":{"command":[""]}}`} {
		r := httptest.NewRecorder()
		s.handleSettings(r, httptest.NewRequest(http.MethodPut, "/v1/settings", strings.NewReader(body)))
		if r.Code != 400 {
			t.Fatalf("accepted invalid configuration: %s", body)
		}
	}
	store.save = func(*config.Config) error { return errors.New("fixture disk failure") }
	r := httptest.NewRecorder()
	s.handleSettings(r, httptest.NewRequest(http.MethodPut, "/v1/settings", strings.NewReader(`{"computer":{"enabled":true}}`)))
	if r.Code != 500 || *store.Snapshot().Computer.Enabled {
		t.Fatal("failed persistence published configuration")
	}
}

func TestComputerApprovalAndImagesOverHTTP(t *testing.T) {
	for _, policy := range []string{"ask", "allow", "deny"} {
		t.Run(policy, func(t *testing.T) {
			store, err := session.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			client := &runControlsClient{tool: "computer_exec", args: `{"action":"discover"}`}
			called := false
			server, err := New(Options{Store: store, EventDir: t.TempDir(), DefaultCWD: t.TempDir(), Factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
				a := agent.New(client, "fixture-model", 100, "fixture", agent.WithComputerConfig(config.ComputerConfig{ApprovalPolicy: policy}))
				a.Vision = true
				a.Tools = []tools.Tool{{Def: tools.ComputerExec().Def, Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
					if err := tools.Authorize(ctx, "computer_exec", "fixture"); err != nil {
						return "", err
					}
					called = true
					tools.AttachImage(ctx, "png", []byte("fixture"))
					return "observed", nil
				}}}
				return a, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			httpServer := httptest.NewServer(server)
			defer httpServer.Close()
			sess := createTestSession(t, httpServer.URL)
			options := protocol.RunOptions{Mode: "agent", ApprovalPolicy: "auto", Tools: &protocol.ToolSelection{Policies: map[string]string{"computer_exec": "allow"}}}
			var accepted protocol.RunAccepted
			resp := postJSON(t, http.DefaultClient, httpServer.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "computer", Prompt: "inspect fixture", Options: &options}, &accepted)
			if resp.StatusCode != 202 {
				t.Fatalf("run: %d", resp.StatusCode)
			}
			events := readToolHistoryRun(t, httpServer.URL, sess.ID, accepted.RunID, "reject")
			result := toolHistoryToolResult(t, events)
			if called != (policy == "allow") {
				t.Fatalf("backend policy %s bypassed", policy)
			}
			if policy == "allow" {
				if len(result.Content) != 1 || result.Content[0].Type != "image" {
					t.Fatalf("lost screenshot: %+v", result)
				}
				redacted := redactTrajectoryEvents(events)
				for _, event := range redacted {
					if event.Type == protocol.EventToolResult && strings.Contains(string(event.Payload), "base64") {
						t.Fatal("screenshot leaked in redacted export")
					}
				}
			}
		})
	}
}
