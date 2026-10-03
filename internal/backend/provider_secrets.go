package backend

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// providerSecretRequest carries the confirmation marker the desktop client sends when the
// user explicitly asks to read back a stored credential.
type providerSecretRequest struct {
	Confirm bool `json:"confirm,omitempty"`
}

// providerSecrets is the reveal response. Values are the resolved plaintext the backend would
// actually send, so the UI shows what a request uses rather than the raw reference (for
// example "${OPENAI_API_KEY}").
type providerSecrets struct {
	APIKey     string            `json:"apiKey,omitempty"`
	UsageQuery *usageQuerySecret `json:"usageQuery,omitempty"`
}

type usageQuerySecret struct {
	APIKey          string `json:"apiKey,omitempty"`
	AccessToken     string `json:"accessToken,omitempty"`
	SecretAccessKey string `json:"secretAccessKey,omitempty"`
}

func providerSecretsID(path string) (string, error) {
	const prefix = "/v1/settings/providers/"
	const suffix = "/secrets"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", errors.New("provider ID is required")
	}
	escapedID := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if escapedID == "" || strings.Contains(escapedID, "/") {
		return "", errors.New("provider ID is required")
	}
	id, err := url.PathUnescape(escapedID)
	if err != nil || id == "" {
		return "", errors.New("provider ID is required")
	}
	return id, nil
}

// storedSecret resolves one credential for display. An unresolvable reference yields an empty
// value instead of failing the whole response, so one broken reference cannot hide the rest.
func storedSecret(ctx context.Context, raw string) string {
	if raw == "" {
		return ""
	}
	resolved, err := config.ResolveSecretContext(ctx, raw)
	if err != nil {
		return ""
	}
	return resolved
}

func usageQuerySecrets(ctx context.Context, provider config.Provider) *usageQuerySecret {
	if len(provider.UsageQuery) == 0 {
		return nil
	}
	read := func(key string) string {
		value, _ := provider.UsageQuery[key].(string)
		return storedSecret(ctx, value)
	}
	out := &usageQuerySecret{
		APIKey:          read("apiKey"),
		AccessToken:     read("accessToken"),
		SecretAccessKey: read("secretAccessKey"),
	}
	if out.APIKey == "" && out.AccessToken == "" && out.SecretAccessKey == "" {
		return nil
	}
	return out
}

// handleProviderSecrets reveals stored credentials after an explicit confirmation. The regular
// settings projection always redacts secrets; this route is the single opt-in exception, and it
// requires confirm=true so a stray request cannot echo a key by accident.
func (s *Server) handleProviderSecrets(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeJSONError(w, http.StatusNotImplemented, "backend settings are unavailable")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id, err := providerSecretsID(r.URL.EscapedPath())
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	var input providerSecretRequest
	if err := decodeJSON(w, r, &input); err != nil {
		return
	}
	if !input.Confirm {
		writeJSONError(w, http.StatusBadRequest, "provider secret reveal requires confirm=true")
		return
	}
	provider, ok := s.settings.Snapshot().Providers[id]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "provider not found")
		return
	}
	writeJSON(w, http.StatusOK, providerSecrets{
		APIKey:     storedSecret(r.Context(), provider.APIKey),
		UsageQuery: usageQuerySecrets(r.Context(), provider),
	})
}
