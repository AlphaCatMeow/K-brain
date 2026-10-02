package backend

import (
	"context"
	"net/http"
	"net/http/httptest"
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
