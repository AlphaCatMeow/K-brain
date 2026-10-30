package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/approval"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

// The run's reasoning is a level the host picked from the model's own level list (LiveAgent
// clamps to it in normalizeChatRuntimeReasoningForLevels). Some models expose a single level:
// LiveAgent's catalog lists gpt-5.2-chat-latest as ["medium"] (off=false), gpt-5-pro as ["high"].
// reviewEffort maps every such run to "low", a value the provider rejects for these models, so
// every review fails and auto review degrades to manual approval for each tool call. Before
// 802a669 the session reviewer sent no effort and these reviews succeeded.
func TestSessionReviewerEffortStaysWithinModelLevels(t *testing.T) {
	var mu sync.Mutex
	var sent []any
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		sent = append(sent, req["reasoning_effort"])
		mu.Unlock()
		// Upstream contract for single-level models: any other reasoning_effort is a 400.
		if effort, ok := req["reasoning_effort"]; ok && effort != "medium" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unsupported value: 'reasoning_effort' does not support 'low' with this model. Supported values are: 'medium'.","type":"invalid_request_error","param":"reasoning_effort","code":"unsupported_value"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": reviewLow}}}})
	}))
	defer provider.Close()
	cfg := &config.Config{
		AIApproval: config.AIApprovalConfig{Mode: "auto"},
		Providers:  map[string]config.Provider{"openai": {API: "openai-completions", BaseURL: provider.URL, APIKey: "fixture-key"}},
		Models:     map[string]config.Model{"gpt-5.2-chat-latest": {Providers: []string{"openai"}}},
	}
	// "medium" is the only reasoning LiveAgent can send for this model.
	reviewer := approval.NewWithFallback(t.Context(), cfg, "openai", "gpt-5.2-chat-latest", reviewEffort("medium"))
	report := reviewer.Review(t.Context(), approval.Evidence{Phase: "action", Tool: "Read", Action: `{"path":"README.md"}`, UserIntent: "read the readme"})
	mu.Lock()
	defer mu.Unlock()
	if !report.Safe() {
		t.Fatalf("session review failed for a single-level model: report=%+v, reasoning_effort sent=%v", report, sent)
	}
}
