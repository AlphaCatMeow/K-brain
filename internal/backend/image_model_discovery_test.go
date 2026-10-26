package backend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// Discovery responses use the same settingsModel shape as GET /v1/settings, so
// imageModel (no omitempty) must carry the same default guess, not a hard false.
func TestReviewDiscoveredModelsCarryImageModelGuess(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"gpt-image-1"},{"id":"chat-model"}]}`)
	}))
	defer upstream.Close()
	store := NewSettingsStore(&config.Config{
		Providers: map[string]config.Provider{"p": {Type: "codex", API: ai.APIChatCompletions, BaseURL: upstream.URL + "/v1", APIKey: "key"}},
		Models:    map[string]config.Model{},
	}, nil)
	sessions, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	server, err := New(Options{Store: sessions, Settings: store, EventDir: t.TempDir(),
		Factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
			return agent.New(&settingsProviderTestClient{}, "m", 100, ""), nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewServer(server)
	defer backend.Close()
	resp, err := backend.Client().Post(backend.URL+"/v1/settings/providers/p/models", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != 200 {
		t.Fatalf("status=%d err=%v", resp.StatusCode, err)
	}
	for _, m := range out.Models {
		if m["id"] == "gpt-image-1" && m["imageModel"] != true {
			t.Fatalf("discovered gpt-image-1 imageModel=%v; GET /v1/settings would report true for the same model", m["imageModel"])
		}
	}
}
