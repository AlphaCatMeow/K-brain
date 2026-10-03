package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func secretsFixture(t *testing.T) *Server {
	t.Helper()
	t.Setenv("KBRAIN_TEST_SECRET", "resolved-from-env")
	cfg := &config.Config{Providers: map[string]config.Provider{
		"p": {
			API:     "openai-completions",
			BaseURL: "https://api.example/v1",
			APIKey:  "sk-stored-plaintext",
			UsageQuery: map[string]any{
				"enabled": true, "mode": "general",
				"apiKey":      "${KBRAIN_TEST_SECRET}",
				"accessToken": "usage-token",
			},
		},
	}}
	return &Server{settings: NewSettingsStore(cfg, func(*config.Config) error { return nil })}
}

func postSecrets(t *testing.T, server *Server, providerID, body string) (int, providerSecrets) {
	t.Helper()
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/settings/providers/"+providerID+"/secrets", strings.NewReader(body)))
	var out providerSecrets
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestProviderSecretsRevealsResolvedValues(t *testing.T) {
	server := secretsFixture(t)
	code, out := postSecrets(t, server, "p", `{"confirm":true}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if out.APIKey != "sk-stored-plaintext" {
		t.Fatalf("apiKey = %q", out.APIKey)
	}
	if out.UsageQuery == nil || out.UsageQuery.APIKey != "resolved-from-env" || out.UsageQuery.AccessToken != "usage-token" {
		t.Fatalf("usageQuery = %+v", out.UsageQuery)
	}
}

// The settings projection must keep redacting; only the explicit reveal reads plaintext back.
func TestProviderSecretsLeavesSettingsRedacted(t *testing.T) {
	server := secretsFixture(t)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/settings", nil))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "sk-stored-plaintext") {
		t.Fatalf("settings leaked a secret: %d %s", rec.Code, rec.Body.String())
	}
}

func TestProviderSecretsRequiresConfirmation(t *testing.T) {
	server := secretsFixture(t)
	if code, _ := postSecrets(t, server, "p", `{}`); code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	if code, _ := postSecrets(t, server, "p", `{"confirm":false}`); code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
}

func TestProviderSecretsUnknownProviderAndMethod(t *testing.T) {
	server := secretsFixture(t)
	if code, _ := postSecrets(t, server, "missing", `{"confirm":true}`); code != http.StatusNotFound {
		t.Fatalf("unknown provider status = %d, want 404", code)
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/settings/providers/p/secrets", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", rec.Code)
	}
}

// An unresolvable reference must not hide the other credentials.
func TestProviderSecretsUnsetEnvironmentReferenceIsEmpty(t *testing.T) {
	os.Unsetenv("KBRAIN_TEST_ABSENT")
	cfg := &config.Config{Providers: map[string]config.Provider{
		"p": {API: "openai-completions", BaseURL: "https://api.example/v1", APIKey: "${KBRAIN_TEST_ABSENT}"},
	}}
	server := &Server{settings: NewSettingsStore(cfg, func(*config.Config) error { return nil })}
	code, out := postSecrets(t, server, "p", `{"confirm":true}`)
	if code != http.StatusOK || out.APIKey != "" {
		t.Fatalf("status = %d apiKey = %q, want empty", code, out.APIKey)
	}
}

// The reveal route sits behind the backend bearer token like every other settings route.
func TestProviderSecretsRequiresBearerToken(t *testing.T) {
	server := secretsFixture(t)
	server.token = "backend-token"
	if code, _ := postSecrets(t, server, "p", `{"confirm":true}`); code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", code)
	}
}
