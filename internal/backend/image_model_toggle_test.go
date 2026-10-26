package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func TestImageModelToggleHealthCapability(t *testing.T) {
	w := imageHTTPRequest(&Server{token: "test-image-token"}, "GET", "/v1/health", nil)
	var v struct {
		Capabilities map[string]any `json:"capabilities"`
	}
	if json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Capabilities["image_model_toggle"] != float64(1) {
		t.Fatal(w.Body.String())
	}
}

func TestImageModelToggleSettingsRoundTrip(t *testing.T) {
	cfg := &config.Config{
		DefaultModel: "chat-model", DefaultProvider: "p",
		Providers: map[string]config.Provider{
			"p": {Name: "Provider", API: "openai-responses", BaseURL: "https://api.example/v1", APIKey: "fake"},
		},
	}
	server := httptest.NewServer(&Server{settings: NewSettingsStore(cfg, func(*config.Config) error { return nil })})
	defer server.Close()
	put := func(models []map[string]any) {
		t.Helper()
		var raw json.RawMessage
		input := map[string]any{"providers": []any{map[string]any{"id": "p", "models": models}}}
		if code := doJSON(t, http.MethodPut, server.URL+"/v1/settings", input, &raw); code != http.StatusOK {
			t.Fatalf("PUT status %d: %s", code, raw)
		}
	}
	get := func() map[string]settingsModel {
		t.Helper()
		var got settingsProjection
		if code := doJSON(t, http.MethodGet, server.URL+"/v1/settings", nil, &got); code != http.StatusOK {
			t.Fatalf("GET status %d", code)
		}
		models := map[string]settingsModel{}
		for _, m := range got.Providers[0].Models {
			models[m.ID] = m
		}
		return models
	}

	put([]map[string]any{{"id": "gpt-image-2.5-flare"}, {"id": "chat-model"}})
	models := get()
	if !models["gpt-image-2.5-flare"].ImageModel || models["chat-model"].ImageModel {
		t.Fatalf("default guess: %+v", models)
	}

	// The read-only flag rides along unchanged, as a GET-edit-PUT client sends it.
	put([]map[string]any{
		{"id": "gpt-image-2.5-flare", "imageAdapter": "disabled", "imageModel": true},
		{"id": "chat-model", "imageAdapter": "enabled", "imageModel": false},
	})
	models = get()
	if models["gpt-image-2.5-flare"].ImageModel || !models["chat-model"].ImageModel {
		t.Fatalf("explicit toggle: %+v", models)
	}

	// Clearing the choice returns to the default guess.
	put([]map[string]any{{"id": "gpt-image-2.5-flare", "imageAdapter": ""}, {"id": "chat-model", "imageAdapter": ""}})
	models = get()
	if !models["gpt-image-2.5-flare"].ImageModel || models["chat-model"].ImageModel {
		t.Fatalf("reset toggle: %+v", models)
	}
}

func TestImageSettingsPruneEmptyProviderConnectionsOnStart(t *testing.T) {
	providerConnection := func(id string) imaging.ImageConnection {
		return imaging.ImageConnection{ID: id, Name: id, Enabled: true, MaxConcurrent: 1, Binding: imaging.ConnectionBinding{Kind: "provider_ref", ProviderID: "p"}}
	}
	used := imageTestTarget()
	used.ConnectionID = "ic_used"
	cfg := config.Default()
	cfg.Image = &imaging.ImageSettings{
		Revision:    7,
		Connections: []imaging.ImageConnection{providerConnection("ic_empty"), providerConnection("ic_used"), imageTestConnection()},
		Targets:     []imaging.ImageTarget{used},
	}
	saves := 0
	store := NewSettingsStore(cfg, func(*config.Config) error { saves++; return nil })
	if _, err := NewImageSettingsManager(store, filepath.Join(t.TempDir(), "secrets.json"), ImageSettingsOptions{}); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range store.cfg.Image.Connections {
		ids = append(ids, c.ID)
	}
	// Only the provider_ref connection without targets goes; an empty independent
	// connection may be mid-setup, so it stays.
	if saves != 1 || store.cfg.Image.Revision != 8 || len(ids) != 2 || ids[0] != "ic_used" || ids[1] != "ic_test" {
		t.Fatalf("saves=%d revision=%d connections=%v", saves, store.cfg.Image.Revision, ids)
	}

	// Nothing left to prune: a second start does not save again.
	if _, err := NewImageSettingsManager(store, filepath.Join(t.TempDir(), "secrets.json"), ImageSettingsOptions{}); err != nil {
		t.Fatal(err)
	}
	if saves != 1 {
		t.Fatalf("saves=%d", saves)
	}
}
