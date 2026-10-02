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
	"github.com/Stack-Cairn/K-brain/internal/routing"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestProviderModelsIDUnescapesEncodedProviderIDs(t *testing.T) {
	for _, tc := range []struct {
		path string
		want string
	}{
		{path: "/v1/settings/providers/saved%2Fprovider/models", want: "saved/provider"},
		{path: "/v1/settings/providers/provider%20with%20spaces/models", want: "provider with spaces"},
		{path: "/v1/settings/providers/provider%25/models", want: "provider%"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			got, err := providerModelsID(tc.path)
			if err != nil || got != tc.want {
				t.Fatalf("providerModelsID(%q) = %q, %v; want %q", tc.path, got, err, tc.want)
			}
		})
	}
	for _, path := range []string{"/v1/settings/providers//models", "/v1/settings/providers/p/models/extra"} {
		if _, err := providerModelsID(path); err == nil {
			t.Fatalf("providerModelsID(%q) unexpectedly succeeded", path)
		}
	}
}

func TestProviderModelDiscoveryAcceptsEncodedProviderIDOverHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Fatalf("upstream request = %s %s", r.Method, r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"model-1","context_length":100,"max_completion_tokens":20}]}`)
	}))
	defer upstream.Close()

	store := NewSettingsStore(&config.Config{
		Providers: map[string]config.Provider{
			"saved/provider": {
				Type:         "codex",
				API:          ai.APIChatCompletions,
				BaseURL:      upstream.URL + "/v1",
				APIKey:       "key",
				ActiveModels: []string{"model-1"},
			},
		},
		Models: map[string]config.Model{},
	}, nil)
	sessions, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	server, err := New(Options{
		Store: sessions,
		Factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
			return agent.New(&settingsProviderTestClient{}, "m", 100, ""), nil
		},
		Settings: store,
		EventDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewServer(server)
	defer backend.Close()

	response, err := backend.Client().Post(
		backend.URL+"/v1/settings/providers/saved%2Fprovider/models",
		"application/json",
		strings.NewReader(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("discovery=%d %s", response.StatusCode, body)
	}
	var result struct {
		Provider string          `json:"provider"`
		Models   []settingsModel `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if result.Provider != "saved/provider" || len(result.Models) != 1 || result.Models[0].ID != "model-1" {
		t.Fatalf("discovery result=%s", body)
	}
}

func TestCustomProviderSettingsRoundTripAndRedaction(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.Provider{"p": {Type: "codex", API: ai.APIChatCompletions, BaseURL: "https://api.example/v1", APIKey: "secret", CustomHeaders: []config.CustomHeader{{Key: "X-Trace", Value: "trace"}, {Key: "X-Token", Value: "secret-header"}}, ActiveModels: []string{"m"}, Metadata: map[string]any{"label": "keep"}}}, Models: map[string]config.Model{"m": {ID: "m", Providers: []string{"p"}, Context: 123, MaxOut: 45}}}
	var saved *config.Config
	store := NewSettingsStore(cfg, func(c *config.Config) error { saved = c.Snapshot(); return nil })
	s := &Server{settings: store}
	out := httptest.NewRecorder()
	s.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/v1/settings", nil))
	if out.Code != 200 || strings.Contains(out.Body.String(), "secret") || !strings.Contains(out.Body.String(), "X-Token") {
		t.Fatalf("projection=%d %s", out.Code, out.Body.String())
	}
	body := `{"providers":[{"id":"p","name":"P","type":"codex","api":"openai-completions","baseUrl":"https://api.example/v1","customHeaders":[{"key":"X-Trace","value":"trace2"},{"key":"X-Token","value":""}],"activeModels":["m"],"models":[{"id":"m","displayName":"M","maxOutputToken":99}]}]}`
	out = httptest.NewRecorder()
	s.ServeHTTP(out, httptest.NewRequest(http.MethodPut, "/v1/settings", strings.NewReader(body)))
	if out.Code != 200 || saved == nil || saved.Providers["p"].APIKey != "secret" || saved.Providers["p"].CustomHeaders[1].Value != "secret-header" || saved.Models["m"].MaxOut != 99 {
		t.Fatalf("save=%d %#v", out.Code, saved)
	}
}

func TestProviderModelDiscoveryUsesBackendHeadersAndFullURL(t *testing.T) {
	for _, tc := range []struct {
		name, api, endpoint, modelsURL, catalog string
		full                                    bool
	}{
		{"custom chat", ai.APIChatCompletions, "/chat", "", "/v1/models", true},
		{"chat completions", ai.APIChatCompletions, "/v1/chat/completions", "", "/v1/models", true},
		{"responses", ai.APIResponses, "/responses", "", "/v1/models", true},
		{"versioned responses", ai.APIResponses, "/v1/responses", "", "/v1/models", true},
		{"response alias", ai.APIResponses, "/response", "", "/v1/models", true},
		{"prefixed query", ai.APIChatCompletions, "/custom/v1/chat/completions?region=cn", "", "/custom/v1/models", true},
		{"custom generation", ai.APIChatCompletions, "/custom/complete?region=cn", "", "/custom/v1/models", true},
		{"trailing slash", ai.APIChatCompletions, "/v1/chat/completions/", "", "/v1/models", true},
		{"escaped prefix", ai.APIChatCompletions, "/custom%2Ftenant/v1/chat/completions", "", "/custom%2Ftenant/v1/models", true},
		{"catalog override", ai.APIChatCompletions, "/chat", "/catalog/?region=cn/", "/catalog/?region=cn/", true},
		{"messages", ai.APIMessages, "/messages", "", "/v1/models", true},
		{"versioned messages", ai.APIMessages, "/custom/v1/messages", "", "/custom/v1/models", true},
		{"gemini", ai.APIGemini, "/v1beta/models/m:generateContent?key=secret", "", "/v1beta/models", true},
		{"gemini catalog override", ai.APIGemini, "/generate", "/catalog/", "/catalog/", true},
		{"legacy base", ai.APIChatCompletions, "/custom/v1", "", "/custom/v1/models", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan *http.Request, 2)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r.Clone(r.Context())
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					if r.URL.RequestURI() != tc.catalog {
						http.Error(w, "not a model catalog", http.StatusNotFound)
						return
					}
					if tc.api == ai.APIGemini {
						_, _ = io.WriteString(w, `{"models":[{"name":"models/m","inputTokenLimit":100,"outputTokenLimit":20}]}`)
					} else {
						_, _ = io.WriteString(w, `{"data":[{"id":"m","context_length":100,"max_completion_tokens":20}]}`)
					}
					return
				}
				if tc.api == ai.APIResponses {
					_, _ = io.WriteString(w, `{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`)
				} else {
					_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
				}
			}))
			defer upstream.Close()
			provider := config.Provider{API: tc.api, BaseURL: upstream.URL + tc.endpoint, IsFullURL: tc.full, APIKey: "k", CustomHeaders: []config.CustomHeader{{Key: "X-Test", Value: "yes"}}}
			if tc.modelsURL != "" {
				provider.ModelsURL = upstream.URL + tc.modelsURL
			}
			store := NewSettingsStore(&config.Config{Providers: map[string]config.Provider{"p": provider}}, nil)
			st, err := session.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			srv, err := New(Options{Store: st, Factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
				return agent.New(&settingsProviderTestClient{}, "m", 100, ""), nil
			}, Settings: store, EventDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			backend := httptest.NewServer(srv)
			defer backend.Close()
			out, err := backend.Client().Post(backend.URL+"/v1/settings/providers/p/models", "application/json", strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			defer out.Body.Close()
			body, err := io.ReadAll(out.Body)
			if err != nil {
				t.Fatal(err)
			}
			if out.StatusCode != http.StatusOK {
				t.Fatalf("discovery=%d %s", out.StatusCode, body)
			}
			got := <-requests
			auth, key := "Authorization", "Bearer k"
			if tc.api == ai.APIMessages {
				auth, key = "X-Api-Key", "k"
			} else if tc.api == ai.APIGemini {
				auth, key = "X-Goog-Api-Key", "k"
			}
			if got.Method != http.MethodGet || got.URL.RequestURI() != tc.catalog || got.Header.Get("X-Test") != "yes" || got.Header.Get(auth) != key {
				t.Fatalf("discovery request=%s %s headers=%v", got.Method, got.URL, got.Header)
			}
			var result struct {
				Models []settingsModel `json:"models"`
			}
			if err := json.Unmarshal(body, &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Models) != 1 || result.Models[0].ID != "m" || result.Models[0].Provider != "p" || result.Models[0].ContextWindow != 100 || result.Models[0].MaxOutputTokens != 20 {
				t.Fatalf("discovered models=%s", body)
			}
			if tc.full && (tc.api == ai.APIChatCompletions || tc.api == ai.APIResponses) {
				client, err := routing.ClientForProviderContext(context.Background(), store.Snapshot().Providers["p"], "p", 1)
				if err != nil {
					t.Fatal(err)
				}
				text, _, err := client.Complete(context.Background(), ai.Request{Model: "m", Messages: []ai.Message{{Role: "user", Content: "hello"}}})
				if err != nil || text != "ok" {
					t.Fatalf("generation=%q %v", text, err)
				}
				got = <-requests
				if got.Method != http.MethodPost || got.URL.RequestURI() != strings.TrimRight(tc.endpoint, "/") || got.Header.Get("X-Test") != "yes" || got.Header.Get("Authorization") != "Bearer k" {
					t.Fatalf("generation request=%s %s headers=%v", got.Method, got.URL, got.Header)
				}
			}
		})
	}
}

type settingsProviderTestClient struct{}

func (*settingsProviderTestClient) Models(context.Context) ([]ai.ModelInfo, error) { return nil, nil }
func (*settingsProviderTestClient) Complete(context.Context, ai.Request) (string, ai.Usage, error) {
	return "", ai.Usage{}, nil
}
func (c *settingsProviderTestClient) Clone() ai.Client { return c }
func (*settingsProviderTestClient) SetCacheKey(string) {}
func (*settingsProviderTestClient) Endpoint() string   { return "fixture" }
func (*settingsProviderTestClient) Stream(context.Context, ai.Request, func(string), func(string), func(string, string, string)) (ai.Message, ai.Usage, error) {
	return ai.Message{}, ai.Usage{}, nil
}
