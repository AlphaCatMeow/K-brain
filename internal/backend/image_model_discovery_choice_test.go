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

// A user's stored per-model choice must win in discovery, as it does in GET /v1/settings.
func TestReviewDiscoveryHonorsStoredImageAdapterChoice(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"gpt-image-1"},{"id":"chat-model"}]}`)
	}))
	defer upstream.Close()
	store := NewSettingsStore(&config.Config{
		Providers: map[string]config.Provider{"p": {Type: "codex", API: ai.APIChatCompletions, BaseURL: upstream.URL + "/v1", APIKey: "key", ActiveModels: []string{"gpt-image-1", "chat-model"}}},
		Models: map[string]config.Model{
			"gpt-image-1": {Providers: []string{"p"}, ImageAdapter: "disabled"},
			"chat-model":  {Providers: []string{"p"}, ImageAdapter: "enabled"},
		},
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
	settingsResp, err := backend.Client().Get(backend.URL + "/v1/settings")
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Providers []struct {
			ID     string           `json:"id"`
			Models []map[string]any `json:"models"`
		} `json:"providers"`
	}
	_ = json.NewDecoder(settingsResp.Body).Decode(&settings)
	settingsResp.Body.Close()
	want := map[string]any{}
	for _, p := range settings.Providers {
		for _, m := range p.Models {
			want[m["id"].(string)] = m["imageModel"]
		}
	}
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
		id := m["id"].(string)
		if m["imageModel"] != want[id] {
			t.Errorf("%s: discovery imageModel=%v, GET /v1/settings imageModel=%v", id, m["imageModel"], want[id])
		}
	}
}
