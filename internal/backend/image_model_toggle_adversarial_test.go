package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/imaging/adapters/direct"
)

func TestImageModelToggleAdversarialNames(t *testing.T) {
	for _, tc := range []struct {
		name, id string
		model    config.Model
		adapter  string
		found    bool
	}{
		{"case-insensitive image name", "GPT-IMAGE-2.5-Flare", config.Model{}, direct.OpenAIID, true},
		{"bare image", "image", config.Model{}, direct.OpenAIID, true},
		// "imagen" contains "image" but needs a different protocol: listed, never executable.
		{"imagen counts but is unsupported", "imagen-4-ultra", config.Model{}, "", true},
		{"dall-e without image in name", "dall-e-3", config.Model{}, "", false},
		{"disabled beats image output", "custom-painter", config.Model{ImageAdapter: "disabled", OutputModalities: []string{"image"}}, "", false},
		{"text output beats image name", "gpt-image-2.5-flare", config.Model{OutputModalities: []string{"text"}}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter, found := imageModelAdapter(config.Provider{API: "openai-responses"}, tc.id, tc.model)
			if adapter != tc.adapter || found != tc.found {
				t.Fatalf("got adapter=%q found=%v", adapter, found)
			}
		})
	}
}

// Unsafe or oversized IDs must be listed as disabled, never reach a request URL.
func TestImageModelToggleUnsafeModelIDsStayDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["p"] = config.Provider{Name: "P", API: "openai-responses", BaseURL: "https://api.example/v1", APIKey: "fake"}
	ids := []string{"gpt-image-" + strings.Repeat("x", 300), "gpt-image/../../admin", "gpt-image?x=1", "gpt image 2"}
	for _, id := range ids {
		cfg.Models[id] = config.Model{Providers: []string{"p"}}
	}
	manager, store := imageProviderManager(t, cfg)
	store.mu.RLock()
	targets, _ := manager.providerImageTargetsLocked()
	store.mu.RUnlock()
	if len(targets) != len(ids) {
		t.Fatalf("got %d targets, want %d", len(targets), len(ids))
	}
	for _, target := range targets {
		if target.Enabled || target.DisabledReason != "unsupported_image_model" {
			t.Fatalf("%q: enabled=%v reason=%q", target.RemoteID, target.Enabled, target.DisabledReason)
		}
	}
}

// Target identity depends only on provider/model, so toggling the choice or
// restarting keeps the workbench selection and draft history attached.
func TestImageModelToggleTargetIdentityIsStable(t *testing.T) {
	build := func(adapter string) string {
		cfg := config.Default()
		cfg.Providers["p"] = config.Provider{Name: "P", API: "openai-responses", BaseURL: "https://api.example/v1", APIKey: "fake"}
		cfg.Models["gpt-image-2.5-flare"] = config.Model{Providers: []string{"p"}, ImageAdapter: adapter}
		manager, store := imageProviderManager(t, cfg)
		store.mu.RLock()
		defer store.mu.RUnlock()
		targets, _ := manager.providerImageTargetsLocked()
		if len(targets) != 1 {
			t.Fatalf("adapter %q: %d targets", adapter, len(targets))
		}
		return targets[0].ID
	}
	auto, enabled, explicit := build(""), build("enabled"), build(direct.OpenAIID)
	if auto != enabled || auto != explicit || auto != build("") {
		t.Fatalf("ids differ: auto=%s enabled=%s explicit=%s", auto, enabled, explicit)
	}
}

// Only exact values are accepted; a near-miss must fail loudly and keep the stored choice.
func TestImageModelToggleRejectsNearMissValues(t *testing.T) {
	cfg := &config.Config{
		DefaultModel: "m", DefaultProvider: "p",
		Providers: map[string]config.Provider{"p": {Name: "P", API: "openai-responses", BaseURL: "https://api.example/v1", APIKey: "fake"}},
	}
	server := httptest.NewServer(&Server{settings: NewSettingsStore(cfg, func(*config.Config) error { return nil })})
	defer server.Close()
	put := func(adapter string) int {
		var raw json.RawMessage
		input := map[string]any{"providers": []any{map[string]any{"id": "p", "models": []map[string]any{{"id": "m", "imageAdapter": adapter}}}}}
		return doJSON(t, http.MethodPut, server.URL+"/v1/settings", input, &raw)
	}
	if code := put("disabled"); code != http.StatusOK {
		t.Fatalf("baseline PUT status %d", code)
	}
	for _, value := range []string{" enabled", "enabled ", "Enabled", "ENABLED", "true", "on", "openai-images ", "dall-e"} {
		if code := put(value); code != http.StatusBadRequest {
			t.Fatalf("%q accepted with status %d", value, code)
		}
	}
	var got settingsProjection
	if code := doJSON(t, http.MethodGet, server.URL+"/v1/settings", nil, &got); code != http.StatusOK {
		t.Fatalf("GET status %d", code)
	}
	for _, m := range got.Providers[0].Models {
		if m.ID == "m" && (m.ImageAdapter != "disabled" || m.ImageModel) {
			t.Fatalf("rejected writes changed the stored choice: %+v", m)
		}
	}
}

// Toggle writes race against settings and target reads from several clients;
// every response must be well formed and the last write must win.
func TestImageModelToggleConcurrentClients(t *testing.T) {
	server := imageHTTPFixture(t)
	store := server.image.Settings.store
	store.mu.Lock()
	cfg := config.Default()
	cfg.Providers["p"] = config.Provider{Name: "P", API: "openai-responses", BaseURL: "https://api.example/v1", APIKey: "fake"}
	cfg.Models["gpt-image-2.5-flare"] = config.Model{Providers: []string{"p"}}
	store.cfg = cfg.Snapshot()
	store.mu.Unlock()
	server.settings = store

	values := []string{"disabled", "enabled", ""}
	done := make(chan string, 64)
	for i := 0; i < 24; i++ {
		go func(i int) {
			switch i % 3 {
			case 0:
				body, _ := json.Marshal(map[string]any{"providers": []any{map[string]any{"id": "p", "models": []map[string]any{{"id": "gpt-image-2.5-flare", "imageAdapter": values[(i/3)%3]}}}}})
				if w := imageHTTPRequest(server, http.MethodPut, "/v1/settings", strings.NewReader(string(body))); w.Code != http.StatusOK {
					done <- "PUT " + w.Body.String()
					return
				}
			case 1:
				var got settingsProjection
				if w := imageHTTPRequest(server, http.MethodGet, "/v1/settings", nil); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil {
					done <- "GET settings " + w.Body.String()
					return
				}
			default:
				var got struct{ Items []map[string]any }
				if w := imageHTTPRequest(server, http.MethodGet, "/v1/image/targets", nil); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil {
					done <- "GET targets " + w.Body.String()
					return
				}
			}
			done <- ""
		}(i)
	}
	for i := 0; i < 24; i++ {
		if msg := <-done; msg != "" {
			t.Fatal(msg)
		}
	}

	body, _ := json.Marshal(map[string]any{"providers": []any{map[string]any{"id": "p", "models": []map[string]any{{"id": "gpt-image-2.5-flare", "imageAdapter": "disabled"}}}}})
	if w := imageHTTPRequest(server, http.MethodPut, "/v1/settings", strings.NewReader(string(body))); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	var targets struct{ Items []map[string]any }
	_ = json.Unmarshal(imageHTTPRequest(server, http.MethodGet, "/v1/image/targets", nil).Body.Bytes(), &targets)
	for _, item := range targets.Items {
		if item["remoteId"] == "gpt-image-2.5-flare" {
			t.Fatal("last write (disabled) did not win")
		}
	}
}
