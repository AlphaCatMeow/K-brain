package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func testUsageSettings(provider config.Provider) *SettingsStore {
	cfg := &config.Config{Providers: map[string]config.Provider{"p": provider}}
	return NewSettingsStore(cfg, nil)
}

func TestProviderUsageCustomScriptUsesMappedArgumentsAndDoesNotCacheTest(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer usage-key" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"balance":7}`))
	}))
	defer server.Close()
	provider := config.Provider{Type: "codex", BaseURL: server.URL, APIKey: "provider-key", Metadata: map[string]any{"usageQuery": map[string]any{
		"enabled": true, "mode": "custom", "baseUrl": server.URL, "apiKey": "usage-key",
		"script": `({request:{url:"{{baseUrl}}",method:"GET",headers:{Authorization:"Bearer {{apiKey}}"}},extractor:function(r){return {planName:"custom",remaining:r.balance,unit:"USD"};}})`,
	}}}
	service := NewProviderUsageService(testUsageSettings(provider))
	result := service.Query(context.Background(), "p", true)
	if result.Error != nil || len(result.Data) != 1 || result.Data[0].Remaining == nil || *result.Data[0].Remaining != 7 {
		t.Fatalf("custom result = %+v", result)
	}
	before := calls
	testResult := service.Test(context.Background(), "p", usageConfig{Mode: "custom", BaseURL: server.URL, APIKey: "usage-key", Script: provider.Metadata["usageQuery"].(map[string]any)["script"].(string)})
	if testResult.Error != nil || calls != before+1 {
		t.Fatalf("test result/calls = %+v/%d", testResult, calls)
	}
	if cached := service.Query(context.Background(), "p", false); cached.Error != nil || calls != before+1 {
		t.Fatalf("cached result/calls = %+v/%d", cached, calls)
	}
}

func TestProviderUsageTransientFailureKeepsLastGoodValue(t *testing.T) {
	var fail bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"balance":3}`))
	}))
	defer server.Close()
	provider := config.Provider{Type: "codex", BaseURL: server.URL, APIKey: "key", Metadata: map[string]any{"usageQuery": map[string]any{
		"enabled": true, "mode": "custom", "baseUrl": server.URL,
		"script": `({request:{url:"{{baseUrl}}",method:"GET"},extractor:function(r){return {remaining:r.balance};}})`,
	}}}
	service := NewProviderUsageService(testUsageSettings(provider))
	if got := service.Query(context.Background(), "p", true); got.Error != nil {
		t.Fatalf("initial result = %+v", got)
	}
	fail = true
	got := service.Query(context.Background(), "p", true)
	if got.Error == nil || !got.IsStale || len(got.Data) != 1 || got.Data[0].Remaining == nil || *got.Data[0].Remaining != 3 {
		t.Fatalf("stale result = %+v", got)
	}
}

// Regression: the provider list shows "usage query is disabled" even though the
// settings editor saved usageQuery.enabled=true. updateProvider stores the block in
// Provider.UsageQuery, so the usage service must read that field as well.
func TestProviderUsageEnabledThroughSavedSettings(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"balance":11}`))
	}))
	defer server.Close()

	cfg := &config.Config{Providers: map[string]config.Provider{"p": {Type: "codex", API: "openai-completions", BaseURL: server.URL, APIKey: "key"}}}
	store := NewSettingsStore(cfg, func(*config.Config) error { return nil })
	backendServer := &Server{settings: store, usage: NewProviderUsageService(store)}

	script := `({request:{url:"{{baseUrl}}",method:"GET"},extractor:function(r){return {remaining:r.balance};}})`
	update := map[string]any{"providers": []any{map[string]any{
		"id": "p", "api": "openai-completions", "baseUrl": server.URL, "models": []any{},
		"usageQuery": map[string]any{"enabled": true, "mode": "custom", "baseUrl": server.URL, "script": script},
	}}}
	body, _ := json.Marshal(update)
	put := httptest.NewRecorder()
	backendServer.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/v1/settings", strings.NewReader(string(body))))
	if put.Code != http.StatusOK {
		t.Fatalf("settings save failed: %d %s", put.Code, put.Body.String())
	}

	response := httptest.NewRecorder()
	backendServer.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/providers/p/usage", strings.NewReader(`{"refresh":true}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("usage request failed: %d %s", response.Code, response.Body.String())
	}
	var result ProviderUsageResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Error != nil || len(result.Data) != 1 || result.Data[0].Remaining == nil || *result.Data[0].Remaining != 11 || calls == 0 {
		t.Fatalf("usage result after settings save = %+v (calls=%d)", result, calls)
	}
}
