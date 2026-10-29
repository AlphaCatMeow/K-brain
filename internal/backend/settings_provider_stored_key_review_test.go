package backend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

// Review regressions for "discovery reuses the stored key after an address change".

// A stored base URL can carry userinfo (hand-edited config.json; config loading does
// not reject it). The projection strips it, and handleProviderModels now maps the
// projected address back to the stored one. updateProvider then validates the stored
// URL with endpointURL, which rejects userinfo, so even a typed key fails with 400.
// Before the change the projected draft address replaced the stored one and a typed
// key worked.
func TestReviewStoredUserinfoAddressStillDiscoversWithTypedKey(t *testing.T) {
	upstream := newStoredKeyUpstream(t)
	stored := strings.Replace(upstream.server.URL, "http://", "http://user:pass@", 1) + "/v1"
	s := storedKeyServer(config.Provider{
		Type: "codex", API: ai.APIChatCompletions, RequestFormat: ai.APIChatCompletions,
		BaseURL: stored, APIKey: "stored-key",
	})
	baseURL, modelsURL, full := projectedProvider(t, s)
	if strings.Contains(baseURL, "user:pass") {
		t.Fatalf("projection leaked userinfo: %q", baseURL)
	}
	out := discover(t, s, map[string]any{
		"type": "codex", "requestFormat": "openai-completions", "baseUrl": baseURL,
		"apiKey": "typed-key", "isFullUrl": full, "modelsUrl": modelsURL, "providerId": "p",
	})
	if out.Code != http.StatusOK {
		t.Fatalf("projected address with a typed key: discovery=%d %s", out.Code, out.Body.String())
	}
}

// The change treats "draft address == projection" as "stored address" only in
// discovery. Saving (PUT /v1/settings, which LiveAgent sends for every provider on
// any settings write) does not, so the same draft resolves to two different
// addresses: discovery succeeds against the stored URL, then the save drops the
// parts the projection hid and the saved provider no longer works.
func TestReviewSaveResolvesProjectedAddressLikeDiscovery(t *testing.T) {
	newServer := func(provider config.Provider) *Server {
		cfg := &config.Config{Providers: map[string]config.Provider{"p": provider}, Models: map[string]config.Model{}}
		return &Server{settings: NewSettingsStore(cfg, func(*config.Config) error { return nil })}
	}
	saveProjection := func(t *testing.T, s *Server) {
		t.Helper()
		baseURL, modelsURL, full := projectedProvider(t, s)
		body, _ := json.Marshal(map[string]any{"providers": []any{map[string]any{
			"id": "p", "type": "codex", "api": "openai-completions", "requestFormat": "openai-completions",
			"baseUrl": baseURL, "isFullUrl": full, "modelsUrl": modelsURL,
		}}})
		out := httptest.NewRecorder()
		s.ServeHTTP(out, httptest.NewRequest(http.MethodPut, "/v1/settings", strings.NewReader(string(body))))
		if out.Code != http.StatusOK {
			t.Fatalf("save=%d %s", out.Code, out.Body.String())
		}
	}

	t.Run("secret query in modelsUrl", func(t *testing.T) {
		catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("api_key") != "catalog-secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"model-1"}]}`)
		}))
		t.Cleanup(catalog.Close)
		s := newServer(config.Provider{
			Type: "codex", API: ai.APIChatCompletions, RequestFormat: ai.APIChatCompletions,
			BaseURL: catalog.URL + "/v1", ModelsURL: catalog.URL + "/catalog?api_key=catalog-secret&region=cn",
			APIKey: "stored-key",
		})
		draft := func() map[string]any {
			baseURL, modelsURL, full := projectedProvider(t, s)
			return map[string]any{
				"type": "codex", "requestFormat": "openai-completions", "baseUrl": baseURL,
				"apiKey": "", "isFullUrl": full, "modelsUrl": modelsURL, "providerId": "p",
			}
		}
		if out := discover(t, s, draft()); out.Code != http.StatusOK {
			t.Fatalf("discovery before save=%d %s", out.Code, out.Body.String())
		}
		saveProjection(t, s)
		if got := s.settings.Snapshot().Providers["p"].ModelsURL; !strings.Contains(got, "api_key=catalog-secret") {
			t.Errorf("save of the unchanged projection dropped the secret catalog query: %q", got)
		}
		if out := discover(t, s, draft()); out.Code != http.StatusOK {
			t.Errorf("discovery succeeded before saving the same draft, fails after: %d %s", out.Code, out.Body.String())
		}
	})

	t.Run("query in a full baseUrl", func(t *testing.T) {
		stored := "https://example.openai.azure.com/openai/deployments/d/chat/completions?api-version=2024-02-01"
		s := newServer(config.Provider{
			Type: "codex", API: ai.APIChatCompletions, RequestFormat: ai.APIChatCompletions,
			BaseURL: stored, IsFullURL: true, APIKey: "stored-key",
		})
		saveProjection(t, s)
		if got := s.settings.Snapshot().Providers["p"].BaseURL; got != stored {
			t.Errorf("save of the unchanged projection rewrote the endpoint: got %q, want %q", got, stored)
		}
	})
}
