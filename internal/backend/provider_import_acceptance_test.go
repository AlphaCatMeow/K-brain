package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestProviderImportAcceptanceSaveReloadDiscoverAndSwitchChat(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		source := strings.TrimPrefix(strings.Split(r.URL.Path, "/")[1], "")
		if r.Header.Get("X-Import-Source") != source || r.Header.Get("Authorization") != "Bearer "+source+"-secret" {
			t.Fatalf("provider headers for %s: authorization=%q source=%q", source, r.Header.Get("Authorization"), r.Header.Get("X-Import-Source"))
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, fmt.Sprintf(`{"data":[{"id":"%s-discovered","context_length":65536,"max_completion_tokens":1024}]}`, source))
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/chat/completions") {
			var body struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"reply:%s\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", body.Model)
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	path := filepath.Join(t.TempDir(), "config.json")
	cfg := &config.Config{Providers: map[string]config.Provider{}, Models: map[string]config.Model{}}
	settings := NewSettingsStore(cfg, func(next *config.Config) error { return next.SaveFile(path) })
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	factory := func(_ context.Context, cwd string, selected protocol.ModelRef) (*agent.Agent, error) {
		provider, ok := settings.Snapshot().Providers[selected.Provider]
		if !ok {
			return nil, fmt.Errorf("provider %q missing after reload", selected.Provider)
		}
		headers := make(map[string]string, len(provider.CustomHeaders))
		for _, header := range provider.CustomHeaders {
			headers[header.Key] = header.Value
		}
		client, err := ai.NewClient(ai.ClientOptions{API: provider.API, BaseURL: provider.BaseURL, APIKey: provider.APIKey, Headers: headers})
		if err != nil {
			return nil, err
		}
		ag := agent.New(client, selected.Model, 4096, "provider acceptance")
		ag.ModelName, ag.Provider, ag.WorkingDir = selected.Model, selected.Provider, cwd
		return ag, nil
	}
	server, err := New(Options{Store: store, Factory: factory, Settings: settings, EventDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	providers := []map[string]any{
		{"id": "ccswitch-acceptance", "name": "CC 验收（ccswitch）", "type": "codex", "api": ai.APIChatCompletions, "baseUrl": upstream.URL + "/cc/v1", "apiKey": "cc-secret", "customHeaders": []map[string]string{{"key": "X-Import-Source", "value": "cc"}}, "activeModels": []string{"cc-imported"}, "models": []map[string]any{{"id": "cc-imported", "contextWindow": 32768}}},
		{"id": "cherry-studio-acceptance", "name": "Cherry 验收（Cherry Studio）", "type": "codex", "api": ai.APIChatCompletions, "baseUrl": upstream.URL + "/cherry/v1", "apiKey": "cherry-secret", "customHeaders": []map[string]string{{"key": "X-Import-Source", "value": "cherry"}}, "activeModels": []string{"cherry-imported"}, "models": []map[string]any{{"id": "cherry-imported", "contextWindow": 32768}}},
	}
	var projection settingsProjection
	if status := doJSON(t, http.MethodPut, httpServer.URL+"/v1/settings", map[string]any{"providers": providers}, &projection); status != http.StatusOK {
		t.Fatalf("import save status=%d", status)
	}
	if len(projection.Providers) != 2 || strings.Contains(fmt.Sprint(projection), "cc-secret") {
		t.Fatalf("saved import projection=%+v", projection)
	}
	var discovery struct {
		Models []settingsModel `json:"models"`
	}
	if status := doJSON(t, http.MethodPost, httpServer.URL+"/v1/settings/providers/ccswitch-acceptance/models", map[string]any{}, &discovery); status != http.StatusOK || len(discovery.Models) != 1 || discovery.Models[0].ID != "cc-discovered" {
		t.Fatalf("CC discovery status=%d models=%+v", status, discovery.Models)
	}

	reloaded, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Providers) != 2 || reloaded.Providers["cherry-studio-acceptance"].APIKey != "cherry-secret" {
		t.Fatalf("reload lost imported providers: %+v", reloaded.Providers)
	}
	settings = NewSettingsStore(reloaded, func(next *config.Config) error { return next.SaveFile(path) })

	sess := createProviderAcceptanceSession(t, httpServer.URL, "ccswitch-acceptance", "cc-imported")
	runRequest(t, httpServer.URL, sess.ID, "cc-chat", "from CC")
	first := waitForRun(t, httpServer.URL, sess.ID, 0)
	if first[len(first)-1].Type != protocol.EventRunCompleted {
		t.Fatalf("CC chat events=%+v", first)
	}
	selected := protocol.ModelRef{Provider: "cherry-studio-acceptance", Model: "cherry-imported"}
	var updated protocol.Session
	body, _ := json.Marshal(protocol.UpdateSessionRequest{Model: &selected})
	if status := patchSession(t, httpServer.URL, sess.ID, string(body), &updated); status != http.StatusOK || updated.Model != selected {
		t.Fatalf("model switch status=%d session=%+v", status, updated)
	}
	runRequest(t, httpServer.URL, sess.ID, "cherry-chat", "from Cherry")
	second := waitForRun(t, httpServer.URL, sess.ID, first[len(first)-1].Seq)
	if second[len(second)-1].Type != protocol.EventRunCompleted {
		t.Fatalf("Cherry chat events=%+v", second)
	}
	loaded := getSession(t, httpServer.URL, sess.ID)
	if loaded.Model != selected || loaded.Messages[len(loaded.Messages)-1].Content[0].Text != "reply:cherry-imported" {
		t.Fatalf("switched chat session=%+v", loaded)
	}
}

func createProviderAcceptanceSession(t *testing.T, base, provider, model string) protocol.Session {
	t.Helper()
	var out protocol.Session
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions", protocol.CreateSessionRequest{CWD: t.TempDir(), Model: protocol.ModelRef{Provider: provider, Model: model}}, &out)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create provider session status=%d", resp.StatusCode)
	}
	return out
}
