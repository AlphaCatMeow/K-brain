package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

const reviewLow = `{"risk":"low","sandbox":"low","sensitive_data":"low","model_behavior":"low","injection":"low","reason":"Within requested scope"}`

func TestAIApprovalHTTPModesAndDenyPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, mode, review, policy, decision string
		permission, changed                  bool
	}{
		{"off", "off", reviewLow, "ask", "reject", true, false},
		{"warnings-preserve-ask", "risk_only", reviewLow, "ask", "reject", true, false},
		{"warnings-preserve-allow", "risk_only", `bad`, "auto", "reject", false, true},
		{"automatic-low", "auto", reviewLow, "ask", "reject", false, true},
		{"automatic-high", "auto", strings.Replace(reviewLow, `"risk":"low"`, `"risk":"high"`, 1), "auto", "reject", true, false},
		{"automatic-invalid", "auto", `broken`, "auto", "reject", true, false},
		{"automatic-human-allow", "auto", `broken`, "ask", "allow_once", true, true},
		{"hard-deny", "auto", reviewLow, "deny", "allow_once", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reviewCalls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reviewCalls.Add(1)
				var req map[string]any
				_ = json.NewDecoder(r.Body).Decode(&req)
				if _, ok := req["tools"]; ok {
					t.Error("reviewer has tools")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": tc.review}}}})
			}))
			defer provider.Close()
			root := t.TempDir()
			target := filepath.Join(root, "marker.txt")
			args, _ := json.Marshal(map[string]string{"path": target, "content": "changed"})
			client := &runControlsClient{tool: "Write", args: string(args)}
			store, err := session.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			server, httpServer := newToolHistoryServer(t, store, t.TempDir(), client)
			server.settings = NewSettingsStore(&config.Config{AIApproval: config.AIApprovalConfig{Mode: tc.mode, Provider: "fixture", Model: "small"}, Providers: map[string]config.Provider{"fixture": {API: "openai-completions", BaseURL: provider.URL, APIKey: "fixture-key"}}, Models: map[string]config.Model{"small": {Providers: []string{"fixture"}}, "fixture-model": {Providers: []string{"fixture"}}}}, nil)
			sess := createTestSession(t, httpServer.URL)
			options := protocol.RunOptions{Mode: "agent", ApprovalPolicy: tc.policy, WorkspaceRoots: []protocol.WorkspaceRoot{{Path: root, Access: "write"}}}
			var accepted protocol.RunAccepted
			resp := postJSON(t, http.DefaultClient, httpServer.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "review", Prompt: "write marker", Options: &options}, &accepted)
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("status=%d", resp.StatusCode)
			}
			events := readToolHistoryRun(t, httpServer.URL, sess.ID, accepted.RunID, tc.decision)
			if hasToolHistoryEvent(events, protocol.EventPermissionRequest) != tc.permission {
				t.Fatalf("wrong approval behavior %+v", events)
			}
			data, _ := os.ReadFile(target)
			if (string(data) == "changed") != tc.changed {
				t.Fatalf("unauthorized or missing mutation %q", data)
			}
			if tc.mode == "off" && reviewCalls.Load() != 0 {
				t.Fatal("off contacted reviewer")
			}
			if tc.mode != "off" && tc.policy != "deny" && reviewCalls.Load() == 0 {
				t.Fatal("no real HTTP review")
			}
			for _, event := range events {
				if event.Type == protocol.EventPermissionRequest && tc.mode != "off" {
					var p protocol.PermissionRequest
					_ = json.Unmarshal(event.Payload, &p)
					if !strings.Contains(p.Description, "AI review") {
						t.Fatal("missing risk description")
					}
				}
			}
		})
	}
}
func TestAIApprovalSettingsValidationAndProjection(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.Provider{"p": {}}, Models: map[string]config.Model{"small": {Providers: []string{"p"}}}}
	for _, mode := range []string{"off", "risk_only", "auto"} {
		c := config.AIApprovalConfig{Mode: mode, Provider: "p", Model: "small"}
		if err := applySettings(cfg, settingsUpdate{AIApproval: &c}); err != nil {
			t.Fatal(err)
		}
		if projectSettings(cfg).AIApproval != c {
			t.Fatal("lost review settings")
		}
	}
	bad := config.AIApprovalConfig{Mode: "auto", Provider: "p", Model: "missing"}
	if applySettings(cfg, settingsUpdate{AIApproval: &bad}) == nil {
		t.Fatal("missing reviewer accepted")
	}
}
