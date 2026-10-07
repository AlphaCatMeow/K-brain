package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestCacheCapabilitiesPersistAndSurviveUnrelatedUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store := NewSettingsStore(&config.Config{Providers: map[string]config.Provider{}, Models: map[string]config.Model{}}, func(c *config.Config) error { return c.SaveFile(path) })
	s := &Server{settings: store}
	put := func(body string, want int) {
		t.Helper()
		r := httptest.NewRecorder()
		s.handleSettings(r, httptest.NewRequest(http.MethodPut, "/v1/settings", strings.NewReader(body)))
		if r.Code != want {
			t.Fatalf("settings: %d %s", r.Code, r.Body.String())
		}
	}
	put(`{"providers":[{"id":"relay","name":"Relay","api":"openai-completions","baseUrl":"https://relay.test/v1","promptCacheRetention":"long","cacheControlFormat":"anthropic","cacheCapabilities":{"supportsPromptCacheKey":false,"supportsLongRetention":true,"sessionAffinityFormat":"openrouter"}}]}`, 200)
	put(`{"providers":[{"id":"relay","name":"Renamed"}]}`, 200)
	loaded, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p := loaded.Providers["relay"]
	if p.CacheCapabilities.SupportsPromptCacheKey == nil || *p.CacheCapabilities.SupportsPromptCacheKey || !*p.CacheCapabilities.SupportsLongRetention || p.CacheControlFormat != "anthropic" {
		t.Fatalf("lost cache policy: %+v", p)
	}
	r := httptest.NewRecorder()
	s.handleSettings(r, httptest.NewRequest(http.MethodGet, "/v1/settings", nil))
	var projection settingsProjection
	if err := json.Unmarshal(r.Body.Bytes(), &projection); err != nil {
		t.Fatal(err)
	}
	if projection.Providers[0].CacheCapabilities.SessionAffinityFormat != "openrouter" {
		t.Fatal("projection lost policy")
	}
	revision := store.Revision()
	put(`{"providers":[{"id":"relay","cacheCapabilities":{"sessionAffinityFormat":"invented"}}]}`, 400)
	put(`{"providers":[{"id":"relay","promptCacheRetention":"forever"}]}`, 400)
	if store.Revision() != revision {
		t.Fatal("invalid update changed settings")
	}
	put(`{"providers":[{"id":"relay","cacheCapabilities":{},"cacheControlFormat":""}]}`, 200)
	if store.Snapshot().Providers["relay"].CacheCapabilities.SupportsPromptCacheKey != nil {
		t.Fatal("cannot reset policy defaults")
	}
}
