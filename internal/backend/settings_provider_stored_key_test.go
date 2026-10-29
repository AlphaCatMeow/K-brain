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

// LiveAgent's provider editor discovers models with the stored key: it sends the
// provider ID, an empty apiKey and the baseUrl/modelsUrl that GET /v1/settings
// projected. Saving a provider keeps its stored key when only the address
// changes, so discovery must do the same rather than drop the key.

type storedKeyUpstream struct {
	server   *httptest.Server
	requests chan *http.Request
}

func newStoredKeyUpstream(t *testing.T) storedKeyUpstream {
	t.Helper()
	requests := make(chan *http.Request, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(r.Context())
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"model-1"}]}`)
	}))
	t.Cleanup(server.Close)
	return storedKeyUpstream{server: server, requests: requests}
}

func storedKeyServer(provider config.Provider) *Server {
	cfg := &config.Config{Providers: map[string]config.Provider{"p": provider}, Models: map[string]config.Model{}}
	return &Server{settings: NewSettingsStore(cfg, nil)}
}

func projectedProvider(t *testing.T, s *Server) (baseURL, modelsURL string, full bool) {
	t.Helper()
	out := httptest.NewRecorder()
	s.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/v1/settings", nil))
	var doc struct {
		Providers []struct {
			BaseURL   string `json:"baseUrl"`
			ModelsURL string `json:"modelsUrl"`
			IsFullURL bool   `json:"isFullUrl"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &doc); err != nil || len(doc.Providers) != 1 {
		t.Fatalf("projection=%s", out.Body.String())
	}
	p := doc.Providers[0]
	return p.BaseURL, p.ModelsURL, p.IsFullURL
}

func discover(t *testing.T, s *Server, draft map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(draft)
	out := httptest.NewRecorder()
	s.ServeHTTP(out, httptest.NewRequest(http.MethodPost, "/v1/settings/providers/p/models", strings.NewReader(string(body))))
	return out
}

func TestStoredKeyDiscoveryTreatsProjectedAddressAsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, catalog string
		full                    bool
	}{
		{"plain base", "/v1", "/v1/models", false},
		{"full url trailing slash", "/v1/chat/completions/", "/v1/models", true},
		{"full url with query", "/custom/v1/chat/completions?region=cn", "/custom/v1/models", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newStoredKeyUpstream(t)
			s := storedKeyServer(config.Provider{
				Type: "codex", API: ai.APIChatCompletions, RequestFormat: ai.APIChatCompletions,
				BaseURL: upstream.server.URL + tc.endpoint, IsFullURL: tc.full, APIKey: "stored-key",
			})
			baseURL, modelsURL, full := projectedProvider(t, s)
			out := discover(t, s, map[string]any{
				"type": "codex", "requestFormat": "openai-completions", "baseUrl": baseURL,
				"apiKey": "", "useSystemProxy": false, "isFullUrl": full,
				"modelsUrl": modelsURL, "providerId": "p", "customHeaders": []any{},
			})
			if out.Code != http.StatusOK {
				t.Fatalf("projected %q: discovery=%d %s", baseURL, out.Code, out.Body.String())
			}
			got := <-upstream.requests
			if got.Header.Get("Authorization") != "Bearer stored-key" || got.URL.Path != tc.catalog {
				t.Fatalf("request %s auth=%q", got.URL.RequestURI(), got.Header.Get("Authorization"))
			}
		})
	}
}

func TestStoredKeyDiscoveryFollowsAnAddressChange(t *testing.T) {
	old := newStoredKeyUpstream(t)
	moved := newStoredKeyUpstream(t)
	s := storedKeyServer(config.Provider{
		Type: "codex", API: ai.APIChatCompletions, RequestFormat: ai.APIChatCompletions,
		BaseURL: old.server.URL + "/v1", APIKey: "stored-key",
		CustomHeaders: []config.CustomHeader{{Key: "X-Tenant", Value: "acme"}},
	})

	out := discover(t, s, map[string]any{
		"type": "codex", "requestFormat": "openai-completions", "baseUrl": moved.server.URL + "/v1",
		"apiKey": "", "providerId": "p",
	})
	if out.Code != http.StatusOK {
		t.Fatalf("address change without a new key: discovery=%d %s", out.Code, out.Body.String())
	}
	got := <-moved.requests
	if got.Header.Get("Authorization") != "Bearer stored-key" || got.Header.Get("X-Tenant") != "acme" {
		t.Fatalf("stored credentials not used at the new address: auth=%q tenant=%q",
			got.Header.Get("Authorization"), got.Header.Get("X-Tenant"))
	}

	out = discover(t, s, map[string]any{
		"type": "codex", "requestFormat": "openai-completions", "baseUrl": moved.server.URL + "/v1",
		"apiKey": "new-key", "providerId": "p",
	})
	if out.Code != http.StatusOK {
		t.Fatalf("address change with a new key: discovery=%d %s", out.Code, out.Body.String())
	}
	if got := <-moved.requests; got.Header.Get("Authorization") != "Bearer new-key" {
		t.Fatalf("a typed key must win over the stored one: auth=%q", got.Header.Get("Authorization"))
	}
	select {
	case r := <-old.requests:
		t.Fatalf("the previous address was contacted: %s", r.URL.RequestURI())
	default:
	}
}
