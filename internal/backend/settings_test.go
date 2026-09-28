package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestSettingsProjectionRedactsAndPersists(t *testing.T) {
	cfg := &config.Config{DefaultModel: "m1", DefaultProvider: "p1", Providers: map[string]config.Provider{"p1": {Name: "Provider", API: "openai-completions", BaseURL: "https://api.example/v1", APIKey: "super-secret", Models: []config.PiModel{{ID: "m1", ContextWindow: 100}}}}, Models: map[string]config.Model{"m1": {ID: "m1", Providers: []string{"p1"}, Context: 100}}}
	var saved *config.Config
	store := NewSettingsStore(cfg, func(next *config.Config) error { saved = next.Snapshot(); return nil })
	st, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ag := func(_ context.Context, _ string, selected protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(&testSettingsClient{}, selected.Model, 100, "system"), nil
	}
	server, err := New(Options{Store: st, Factory: ag, Settings: store, EventDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRecorder()
	server.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/settings", nil))
	if get.Code != http.StatusOK || strings.Contains(get.Body.String(), "super-secret") {
		t.Fatalf("redaction failed: %d %s", get.Code, get.Body.String())
	}
	var projection settingsProjection
	if err := json.Unmarshal(get.Body.Bytes(), &projection); err != nil {
		t.Fatal(err)
	}
	if !projection.Providers[0].APIKeyConfigured {
		t.Fatal("expected configured key marker")
	}
	body := `{"defaultModel":"m2","providers":[{"id":"p1","api":"openai-completions","baseUrl":"https://api.example/v1","models":[{"id":"m2","contextWindow":200}]}]}`
	put := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v1/settings", strings.NewReader(body))
	server.ServeHTTP(put, req)
	if put.Code != http.StatusOK || saved == nil || saved.Providers["p1"].APIKey != "super-secret" {
		t.Fatalf("update failed: %d %v", put.Code, saved)
	}
	if strings.Contains(put.Body.String(), "super-secret") {
		t.Fatal("update response leaked key")
	}
	models := httptest.NewRecorder()
	server.ServeHTTP(models, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if models.Code != http.StatusOK || !strings.Contains(models.Body.String(), `"model":"m2"`) || strings.Contains(models.Body.String(), "super-secret") {
		t.Fatalf("model catalog was not refreshed safely: %d %s", models.Code, models.Body.String())
	}

	textRequest := func(provider, model string) *httptest.ResponseRecorder {
		body := `{"model":{"provider":"` + provider + `","model":"` + model + `"},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/text/generate", strings.NewReader(body)))
		return response
	}
	if response := textRequest("p1", "m2"); response.Code != http.StatusOK {
		t.Fatalf("new model was not immediately available for p1: %d %s", response.Code, response.Body.String())
	}

	body = `{"providers":[{"id":"p1","api":"openai-completions","baseUrl":"https://api.example/v1","models":[]}]}`
	put = httptest.NewRecorder()
	server.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/v1/settings", strings.NewReader(body)))
	if put.Code != http.StatusOK {
		t.Fatalf("model deletion failed: %d %s", put.Code, put.Body.String())
	}
	if response := textRequest("p1", "m2"); response.Code != http.StatusBadRequest {
		t.Fatalf("deleted model was accepted: %d %s", response.Code, response.Body.String())
	}

	body = `{"providers":[{"id":"p1","api":"openai-completions","baseUrl":"https://api.example/v1","models":[{"id":"m2","contextWindow":200}]},{"id":"p2","api":"openai-completions","baseUrl":"https://api-two.example/v1","models":[{"id":"m2","contextWindow":200}]}]}`
	put = httptest.NewRecorder()
	server.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/v1/settings", strings.NewReader(body)))
	if put.Code != http.StatusOK {
		t.Fatalf("same model ID across providers was rejected: %d %s", put.Code, put.Body.String())
	}
	if response := textRequest("p2", "m2"); response.Code != http.StatusOK {
		t.Fatalf("same model ID was not immediately available for p2: %d %s", response.Code, response.Body.String())
	}

	body = `{"deleteProviders":["p1"],"providers":[{"id":"p2","api":"openai-completions","baseUrl":"https://api-two.example/v1","models":[{"id":"m2","contextWindow":200}]}]}`
	put = httptest.NewRecorder()
	server.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/v1/settings", strings.NewReader(body)))
	if put.Code != http.StatusOK {
		t.Fatalf("provider deletion failed: %d %s", put.Code, put.Body.String())
	}
	if response := textRequest("p1", "m2"); response.Code != http.StatusBadRequest {
		t.Fatalf("deleted provider model was accepted: %d %s", response.Code, response.Body.String())
	}
	if response := textRequest("p2", "m2"); response.Code != http.StatusOK {
		t.Fatalf("remaining provider model was rejected: %d %s", response.Code, response.Body.String())
	}
}

type testSettingsClient struct{}

func (*testSettingsClient) Models(context.Context) ([]ai.ModelInfo, error) { return nil, nil }
func (c *testSettingsClient) Complete(context.Context, ai.Request) (string, ai.Usage, error) {
	return "", ai.Usage{}, nil
}
func (c *testSettingsClient) Clone() ai.Client { return c }
func (*testSettingsClient) SetCacheKey(string) {}
func (*testSettingsClient) Endpoint() string   { return "https://fixture.invalid" }
func (*testSettingsClient) Stream(context.Context, ai.Request, func(string), func(string), func(string, string, string)) (ai.Message, ai.Usage, error) {
	return ai.Message{}, ai.Usage{}, nil
}

func TestSettingsGETRedactsLegacyURL(t *testing.T) {
	for _, endpoint := range []string{"https://private-user:private-password@api.example/v1?key=private-query#private-fragment", "https://%zz:private-password@api.example/v1"} {
		cfg := &config.Config{Providers: map[string]config.Provider{"p": {BaseURL: endpoint, APIKey: "private-key"}}}
		server := &Server{settings: NewSettingsStore(cfg, nil)}
		out := httptest.NewRecorder()
		server.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/v1/settings", nil))
		if out.Code != http.StatusOK || strings.Contains(out.Body.String(), "private-") {
			t.Fatalf("unsafe projection: %d %s", out.Code, out.Body.String())
		}
		if cfg.Providers["p"].BaseURL != endpoint {
			t.Fatal("projection mutated configuration")
		}
		var doc settingsProjection
		if err := json.Unmarshal(out.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Providers[0].Models == nil {
			t.Fatal("empty model list must be an array")
		}
	}
}

func TestSettingsRefreshSameSessionAfterActiveRun(t *testing.T) {
	reached, release := make(chan string, 4), make(chan struct{})
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached <- r.Header.Get("Authorization")
		<-release
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"old\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer old.Close()
	fresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"new\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer fresh.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := &config.Config{Providers: map[string]config.Provider{"fixture": {API: "openai-completions", BaseURL: old.URL, APIKey: "old-key"}}, Models: map[string]config.Model{"fixture-model": {ID: "fixture-model", Providers: []string{"fixture"}}}}
	settings := NewSettingsStore(cfg, func(c *config.Config) error { return c.SaveFile(path) })
	st, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	factory := func(_ context.Context, cwd string, selected protocol.ModelRef) (*agent.Agent, error) {
		p := settings.Snapshot().Providers[selected.Provider]
		c, err := ai.NewClient(ai.ClientOptions{API: p.API, BaseURL: p.BaseURL, APIKey: p.APIKey})
		if err != nil {
			return nil, err
		}
		a := agent.New(c, selected.Model, 1024, "system")
		a.WorkingDir = cwd
		a.ModelName = selected.Model
		a.Provider = selected.Provider
		return a, nil
	}
	server, err := New(Options{Store: st, Factory: factory, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	sess := createTestSession(t, httpServer.URL)
	rt := server.sessions[sess.ID]
	originalAgent := rt.agent
	originalTasks := rt.agent.Tasks()
	originalRecorder := rt.recorder
	runRequest(t, httpServer.URL, sess.ID, "first", "first")
	select {
	case got := <-reached:
		if got != "Bearer old-key" {
			t.Fatalf("old auth %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first upstream not reached")
	}
	update := map[string]any{"providers": []any{map[string]any{"id": "fixture", "api": "openai-completions", "baseUrl": fresh.URL, "apiKey": "new-key", "models": []any{map[string]any{"id": "fixture-model"}}}}}
	if code := doJSON(t, http.MethodPut, httpServer.URL+"/v1/settings", update, nil); code != 200 {
		t.Fatalf("save %d", code)
	}
	if rt.agent != originalAgent || rt.recorder != originalRecorder {
		t.Fatal("active run replaced")
	}
	close(release)
	events := waitForRun(t, httpServer.URL, sess.ID, 0)
	runRequest(t, httpServer.URL, sess.ID, "second", "continue")
	waitForRun(t, httpServer.URL, sess.ID, events[len(events)-1].Seq)
	if got := <-reached; got != "Bearer new-key" {
		t.Fatalf("new auth %q", got)
	}
	if rt.agent != originalAgent || rt.agent.Tasks() != originalTasks || rt.recorder != originalRecorder {
		t.Fatal("refresh lost session state")
	}
	if len(rt.agent.MessagesSnapshot()) != 5 {
		t.Fatalf("history lost: %v", rt.agent.MessagesSnapshot())
	}
	reloaded, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Providers["fixture"].APIKey != "new-key" || reloaded.Providers["fixture"].BaseURL != fresh.URL {
		t.Fatal("configuration did not persist")
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Mode().Perm() != 0600 {
		t.Fatalf("config mode %v", stat.Mode())
	}
	if code := doJSON(t, http.MethodPut, httpServer.URL+"/v1/settings", map[string]any{"deleteProviders": []string{"fixture"}}, nil); code != 200 {
		t.Fatalf("delete %d", code)
	}
	if code := doJSON(t, http.MethodPost, httpServer.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ClientRequestID: "deleted", Prompt: "no"}, nil); code != 400 {
		t.Fatalf("deleted model run: %d", code)
	}
	if len(rt.agent.MessagesSnapshot()) != 5 {
		t.Fatal("rejected run altered history")
	}
}
