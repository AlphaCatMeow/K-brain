package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestRunPermissionModesExecuteOrReject(t *testing.T) {
	for _, tc := range []struct {
		mode, risk string
		changed    bool
	}{
		{"normal", "low", false}, {"plan", "low", false}, {"auto_edit", "low", true},
		{"full_access", "low", true}, {"always", "unknown", true},
		{"auto_review", "low", true}, {"auto_review", "unknown", false},
	} {
		t.Run(tc.mode+"/"+tc.risk, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("K_BRAIN_HOME", home)
			target := filepath.Join(home, "marker.txt")
			var calls, reviews atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req ai.Request
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if req.Model == "review" {
					reviews.Add(1)
					report := fmt.Sprintf(`{"risk":%q,"sandbox":%q,"sensitive_data":%q,"model_behavior":%q,"injection":%q,"reason":"fixture"}`, tc.risk, tc.risk, tc.risk, tc.risk, tc.risk)
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": report}}}})
					return
				}
				var tool string
				for _, available := range req.Tools {
					if strings.EqualFold(available.Function.Name, "write") {
						tool = available.Function.Name
					}
				}
				delta := map[string]any{"content": "done"}
				if calls.Add(1) == 1 && tool != "" {
					args, _ := json.Marshal(map[string]string{"path": target, "content": "changed"})
					delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "write-fixture", "type": "function", "function": map[string]any{"name": tool, "arguments": string(args)}}}}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": "stop"}}})
				_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
			}))
			defer srv.Close()
			cfg := map[string]any{"defaultModel": "main", "aiApproval": map[string]any{"mode": "off", "provider": "fixture", "model": "review"}, "providers": map[string]any{"fixture": map[string]any{"api": "openai-completions", "baseUrl": srv.URL, "apiKey": "fixture-key", "models": []any{map[string]any{"id": "main", "maxTokens": 256}, map[string]any{"id": "review", "maxTokens": 256}}}}}
			data, _ := json.Marshal(cfg)
			if err := os.WriteFile(filepath.Join(home, "config.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := runCapture(t, "", "--no-session", "--quiet", "--permission-mode", tc.mode, "write the fixture marker"); err != nil {
				t.Fatal(err)
			}
			content, _ := os.ReadFile(target)
			if (string(content) == "changed") != tc.changed {
				t.Fatalf("mutation=%q calls=%d", content, calls.Load())
			}
			if (reviews.Load() > 0) != (tc.mode == "auto_review") {
				t.Fatalf("review calls=%d", reviews.Load())
			}
		})
	}
}
