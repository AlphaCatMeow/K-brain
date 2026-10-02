package backend

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestSettingsClearKeyAndDeleteModel(t *testing.T) {
	cfg := &config.Config{DefaultModel: "m", DefaultProvider: "p", Providers: map[string]config.Provider{"p": {API: "openai-completions", BaseURL: "https://api.example/v1", APIKey: "secret"}}, Models: map[string]config.Model{"m": {ID: "m", Providers: []string{"p"}, Context: 100}}}
	var saved *config.Config
	store := NewSettingsStore(cfg, func(next *config.Config) error { saved = next.Snapshot(); return nil })
	server := &Server{settings: store}
	clear := httptest.NewRecorder()
	server.ServeHTTP(clear, httptest.NewRequest(http.MethodPut, "/v1/settings", strings.NewReader(`{"providers":[{"id":"p","api":"openai-completions","baseUrl":"https://api.example/v1","clearApiKey":true,"models":[]}]}`)))
	if clear.Code != http.StatusOK || saved.Providers["p"].APIKey != "" || len(saved.Models) != 0 {
		t.Fatalf("clear/delete = %d %#v", clear.Code, saved)
	}
	if strings.Contains(clear.Body.String(), "secret") {
		t.Fatal("key leaked")
	}
}

func TestSettingsBlankKeyRetainsKey(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.Provider{"p": {API: "openai-completions", BaseURL: "https://api.example/v1", APIKey: "secret"}}, Models: map[string]config.Model{"m": {ID: "m", Providers: []string{"p"}}}}
	var saved *config.Config
	store := NewSettingsStore(cfg, func(next *config.Config) error { saved = next.Snapshot(); return nil })
	server := &Server{settings: store}
	req := httptest.NewRequest(http.MethodPut, "/v1/settings", strings.NewReader(`{"providers":[{"id":"p","api":"openai-completions","baseUrl":"https://api.example/v1","models":[{"id":"m"}]}]}`))
	out := httptest.NewRecorder()
	server.ServeHTTP(out, req)
	if out.Code != http.StatusOK || saved.Providers["p"].APIKey != "secret" {
		t.Fatalf("blank key cleared: %d %#v", out.Code, saved)
	}
}

func TestSettingsRejectsSharedModelMetadataConflict(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.Provider{"a": {API: "openai-completions", BaseURL: "https://a.invalid/v1"}, "b": {API: "openai-completions", BaseURL: "https://b.invalid/v1"}}, Models: map[string]config.Model{"m": {ID: "m", Providers: []string{"a", "b"}, Context: 100}}}
	var saved *config.Config
	store := NewSettingsStore(cfg, func(next *config.Config) error { saved = next.Snapshot(); return nil })
	server := httptest.NewServer(&Server{settings: store})
	defer server.Close()
	req, err := http.NewRequest(http.MethodPut, server.URL+"/v1/settings", strings.NewReader(`{"providers":[{"id":"a","api":"openai-completions","baseUrl":"https://a.invalid/v1","models":[{"id":"m","contextWindow":200}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Body.Close()
	body, err := io.ReadAll(out.Body)
	if err != nil {
		t.Fatal(err)
	}
	if out.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "identical metadata") {
		t.Fatalf("conflict = %d %s", out.StatusCode, body)
	}
	if saved != nil || !slices.Equal(store.Snapshot().Models["m"].Providers, []string{"a", "b"}) || store.Snapshot().Models["m"].Context != 100 {
		t.Fatalf("rejected update changed shared model: saved=%#v current=%#v", saved, store.Snapshot().Models["m"])
	}
}
