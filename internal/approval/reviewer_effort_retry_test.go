package approval

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// A 400 that has nothing to do with the effort (here a content-filter refusal of one review's
// evidence, which tool output with injected text is exactly what triggers) fails again on the
// retry without effort. That proves the effort was not the cause, yet effortRejected is already
// set, so every later review of the run drops "low" and the session model reasons at its full
// default again: the 15s review timeouts 802a669 fixed come back for the rest of the run.
func TestUnrelated400KeepsReviewEffort(t *testing.T) {
	var mu sync.Mutex
	type call struct {
		filtered bool
		effort   any
	}
	var calls []call
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		raw, _ := json.Marshal(req["messages"])
		filtered := strings.Contains(string(raw), "FILTERED-CONTENT")
		mu.Lock()
		calls = append(calls, call{filtered, req["reasoning_effort"]})
		mu.Unlock()
		if filtered {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"The response was filtered due to the prompt triggering content management policy.","type":"invalid_request_error","param":"prompt","code":"content_filter"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": low}}}})
	}))
	defer provider.Close()
	cfg := &config.Config{
		AIApproval: config.AIApprovalConfig{Mode: "auto"},
		Providers:  map[string]config.Provider{"fixture": {API: "openai-completions", BaseURL: provider.URL, APIKey: "fixture-key"}},
		Models:     map[string]config.Model{"session-model": {Providers: []string{"fixture"}}},
	}
	reviewer := NewWithFallback(t.Context(), cfg, "fixture", "session-model", "low")

	if report := reviewer.Review(t.Context(), Evidence{Phase: "tool_output", Tool: "Read", Action: `{"path":"a.txt"}`, Output: "FILTERED-CONTENT", UserIntent: "read a.txt"}); report.Safe() {
		t.Fatalf("filtered review passed: %+v", report)
	}
	if report := reviewer.Review(t.Context(), Evidence{Phase: "action", Tool: "Read", Action: `{"path":"b.txt"}`, UserIntent: "read b.txt"}); !report.Safe() {
		t.Fatalf("later review failed: %+v", report)
	}
	mu.Lock()
	defer mu.Unlock()
	last := calls[len(calls)-1]
	if last.filtered || last.effort != "low" {
		t.Fatalf("a 400 unrelated to effort dropped the effort for later reviews: calls=%+v", calls)
	}
}
