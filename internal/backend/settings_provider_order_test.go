package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestSettingsProviderOrderPersistsAcrossSaveAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := &config.Config{Providers: map[string]config.Provider{
		"a": {Name: "A", API: "openai-completions", BaseURL: "https://a.invalid/v1"},
		"b": {Name: "B", API: "openai-completions", BaseURL: "https://b.invalid/v1"},
		"c": {Name: "C", API: "openai-completions", BaseURL: "https://c.invalid/v1"},
	}}
	save := func(next *config.Config) error { return next.SaveFile(path) }
	server := httptest.NewServer(&Server{settings: NewSettingsStore(cfg, save)})
	defer server.Close()

	provider := func(id string) map[string]any {
		return map[string]any{"id": id, "api": "openai-completions", "baseUrl": "https://" + id + ".invalid/v1"}
	}
	ids := func(projection settingsProjection) []string {
		out := make([]string, 0, len(projection.Providers))
		for _, p := range projection.Providers {
			out = append(out, p.ID)
		}
		return out
	}
	put := func(update map[string]any, want ...string) {
		t.Helper()
		var raw json.RawMessage
		if code := doJSON(t, http.MethodPut, server.URL+"/v1/settings", update, &raw); code != http.StatusOK {
			t.Fatalf("PUT /v1/settings: status %d: %s", code, raw)
		}
		var got settingsProjection
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(ids(got), want) {
			t.Fatalf("provider order = %v, want %v", ids(got), want)
		}
	}

	// A full provider list is the user's ordering, not an ID-sorted set.
	put(map[string]any{"providers": []any{provider("c"), provider("a"), provider("b")}}, "c", "a", "b")

	reloaded, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := projectSettings(reloaded); !slices.Equal(ids(got), []string{"c", "a", "b"}) {
		t.Fatalf("order after reload = %v", ids(got))
	}
	if md := reloaded.Providers["c"].Metadata; md != nil {
		t.Fatalf("storage-only order entry leaked into provider metadata: %#v", md)
	}

	// Partial updates (e.g. importing only missing providers) keep the order and append.
	put(map[string]any{"providers": []any{provider("d")}}, "c", "a", "b", "d")
	// Deleting keeps the remaining order.
	put(map[string]any{"deleteProviders": []string{"a"}}, "c", "b", "d")
	// Reordering again after a delete.
	put(map[string]any{"providers": []any{provider("d"), provider("b"), provider("c")}}, "d", "b", "c")
}
