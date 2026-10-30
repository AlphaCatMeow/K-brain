package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// With review_scope "per_run" (sent by hosts that hide the global AI approval setting) the
// permission mode picked in the composer decides. Auto review reviews with the configured
// override model or, without a usable one, the session's own model; every other mode ignores
// the global mode. Without the opt-in, ai_approval_test.go keeps the global semantics.
func TestAIApprovalPerRunModeAndSessionModel(t *testing.T) {
	high := strings.Replace(reviewLow, `"risk":"low"`, `"risk":"high"`, 1)
	for _, tc := range []struct {
		name, review, permissionMode string
		approval                     config.AIApprovalConfig
		permission, changed          bool
		reviewModel                  string // "" means the reviewer must not be contacted
	}{
		{"auto-review-uses-session-model", reviewLow, "auto_review", config.AIApprovalConfig{Mode: "off"}, false, true, "fixture-model"},
		{"auto-review-falls-back-from-missing-override", reviewLow, "auto_review", config.AIApprovalConfig{Mode: "off", Provider: "gone", Model: "missing"}, false, true, "fixture-model"},
		{"auto-review-uses-valid-override", reviewLow, "auto_review", config.AIApprovalConfig{Mode: "off", Provider: "fixture", Model: "small"}, false, true, "small"},
		{"normal-ignores-global-auto", reviewLow, "normal", config.AIApprovalConfig{Mode: "auto", Provider: "fixture", Model: "small"}, true, false, ""},
		{"auto-edit-ignores-global-auto", high, "auto_edit", config.AIApprovalConfig{Mode: "auto", Provider: "fixture", Model: "small"}, false, true, ""},
		{"full-access-ignores-global-auto", high, "full_access", config.AIApprovalConfig{Mode: "auto", Provider: "fixture", Model: "small"}, false, true, ""},
		{"unset-mode-ignores-global-auto", high, "", config.AIApprovalConfig{Mode: "auto", Provider: "fixture", Model: "small"}, true, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var models []string
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req map[string]any
				_ = json.NewDecoder(r.Body).Decode(&req)
				mu.Lock()
				// Route resolution may list models first; only chat requests name a model.
				if model, _ := req["model"].(string); model != "" {
					models = append(models, model)
				}
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": tc.review}}}})
			}))
			defer provider.Close()
			root := t.TempDir()
			target := filepath.Join(root, "marker.txt")
			args, _ := json.Marshal(map[string]string{"path": target, "content": "changed"})
			store, err := session.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			server, httpServer := newToolHistoryServer(t, store, t.TempDir(), &runControlsClient{tool: "Write", args: string(args)})
			server.settings = NewSettingsStore(&config.Config{AIApproval: tc.approval, Providers: map[string]config.Provider{"fixture": {API: "openai-completions", BaseURL: provider.URL, APIKey: "fixture-key"}}, Models: map[string]config.Model{"small": {Providers: []string{"fixture"}}, "fixture-model": {Providers: []string{"fixture"}}}}, nil)
			sess := createTestSession(t, httpServer.URL)
			options := protocol.RunOptions{Mode: "agent", ApprovalPolicy: "ask", PermissionMode: tc.permissionMode, ReviewScope: "per_run", WorkspaceRoots: []protocol.WorkspaceRoot{{Path: root, Access: "write"}}}
			var accepted protocol.RunAccepted
			resp := postJSON(t, http.DefaultClient, httpServer.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "review", Prompt: "write marker", Options: &options}, &accepted)
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("status=%d", resp.StatusCode)
			}
			events := readToolHistoryRun(t, httpServer.URL, sess.ID, accepted.RunID, "reject")
			if hasToolHistoryEvent(events, protocol.EventPermissionRequest) != tc.permission {
				t.Fatalf("permission requested = %v, want %v", !tc.permission, tc.permission)
			}
			data, _ := os.ReadFile(target)
			if (string(data) == "changed") != tc.changed {
				t.Fatalf("file content %q, want changed=%v", data, tc.changed)
			}
			mu.Lock()
			defer mu.Unlock()
			if tc.reviewModel == "" && len(models) != 0 {
				t.Fatalf("reviewer contacted with %v", models)
			}
			if tc.reviewModel != "" && (len(models) == 0 || models[0] != tc.reviewModel) {
				t.Fatalf("reviewer models %v, want %s", models, tc.reviewModel)
			}
		})
	}
}

// Hosts hide the global setting only when the backend reviews per run; an older backend
// still applies the global mode to every permission mode and needs a configured model.
func TestAIApprovalPerRunIsAdvertised(t *testing.T) {
	caps := healthJSON(t, &Server{token: healthTestToken, image: &ImageModule{}})["capabilities"].(map[string]any)
	if fmt.Sprint(caps["ai_review_per_run"]) != "1" {
		t.Fatalf("ai_review_per_run = %v", caps["ai_review_per_run"])
	}
	raw, _ := json.Marshal(projectSettings(&config.Config{}))
	var doc struct {
		AIApproval map[string]any `json:"aiApproval"`
	}
	_ = json.Unmarshal(raw, &doc)
	if doc.AIApproval["perRun"] != true || doc.AIApproval["mode"] != "off" {
		t.Fatalf("settings aiApproval = %v", doc.AIApproval)
	}
}
