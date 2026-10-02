package backend

import (
	"encoding/json"
	"net/http"
	"strings"
)

type providerUsageRequest struct {
	Refresh bool `json:"refresh,omitempty"`
}

type providerUsageTestRequest struct {
	Config usageConfig `json:"config"`
}

func (s *Server) providerUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	const prefix = "/v1/providers/"
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/"), "/")
	providerID := ""
	test := false
	switch {
	case len(parts) == 2 && parts[0] != "" && parts[1] == "usage":
		providerID = parts[0]
	case len(parts) == 3 && parts[0] != "" && parts[1] == "usage" && parts[2] == "test":
		providerID, test = parts[0], true
	default:
		writeJSONError(w, http.StatusNotFound, "provider usage route not found")
		return
	}
	if s.usage == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "provider usage is unavailable")
		return
	}
	if !s.usage.ProviderExists(providerID) {
		writeJSONError(w, http.StatusNotFound, "provider not found")
		return
	}
	if !test {
		var input providerUsageRequest
		if err := decodeJSON(w, r, &input); err != nil {
			return
		}
		writeJSON(w, http.StatusOK, s.usage.Query(r.Context(), providerID, input.Refresh))
		return
	}
	var input providerUsageTestRequest
	if err := decodeJSON(w, r, &input); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, s.usage.Test(r.Context(), providerID, input.Config))
}

func decodeProviderUsageTest(w http.ResponseWriter, r *http.Request) (usageConfig, bool) {
	var input providerUsageTestRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid provider usage test request")
		return usageConfig{}, false
	}
	return input.Config, true
}
