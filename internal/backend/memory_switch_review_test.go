package backend

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// Review probe: repeated values and memory combined with another settings field.
func TestReviewMemorySwitchPutSequences(t *testing.T) {
	f := newMemorySwitchFixture(t)
	hasKey := func() bool {
		t.Helper()
		data, err := os.ReadFile(f.configPath)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Contains(string(data), `"memory"`)
	}
	f.setEnabled(t, true)
	f.setEnabled(t, true)
	if hasKey() {
		t.Fatal("on twice wrote memory")
	}
	f.setEnabled(t, false)
	f.setEnabled(t, false)
	if !hasKey() {
		t.Fatal("off twice dropped memory")
	}
	// memory back on together with a provider change in one PUT.
	var projection settingsProjection
	body := map[string]any{
		"memory":    map[string]any{"enabled": true},
		"providers": []any{map[string]any{"id": "extra", "api": "openai-completions", "baseUrl": "http://extra.invalid", "models": []any{map[string]any{"id": "extra-model"}}}},
	}
	if code := doJSON(t, http.MethodPut, f.url+"/v1/settings", body, &projection); code != http.StatusOK || !projection.Memory.Enabled {
		t.Fatalf("combined PUT status %d memory %+v", code, projection.Memory)
	}
	if hasKey() {
		t.Fatal("combined PUT kept memory")
	}
	cfg, err := config.LoadFile(f.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Providers["extra"]; !ok || !cfg.Memory.IsEnabled() {
		t.Fatalf("combined PUT result providers=%v enabled=%v", len(cfg.Providers), cfg.Memory.IsEnabled())
	}
	// off together with a provider delete keeps the off state on disk.
	body = map[string]any{"memory": map[string]any{"enabled": false}, "deleteProviders": []string{"extra"}}
	if code := doJSON(t, http.MethodPut, f.url+"/v1/settings", body, &projection); code != http.StatusOK || projection.Memory.Enabled {
		t.Fatalf("combined off PUT status %d memory %+v", code, projection.Memory)
	}
	if cfg, err = config.LoadFile(f.configPath); err != nil || cfg.Memory.IsEnabled() {
		t.Fatalf("combined off reload enabled=%v err=%v", cfg != nil && cfg.Memory.IsEnabled(), err)
	}
	// Unrelated PUT must not flip or drop the off state.
	if code := doJSON(t, http.MethodPut, f.url+"/v1/settings", map[string]any{"defaultModel": "fixture-model"}, &projection); code != http.StatusOK || projection.Memory.Enabled {
		t.Fatalf("unrelated PUT status %d memory %+v", code, projection.Memory)
	}
	if !hasKey() {
		t.Fatal("unrelated PUT dropped memory off")
	}
}
