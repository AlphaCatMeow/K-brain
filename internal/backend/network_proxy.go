package backend

import (
	"net/http"

	"github.com/Stack-Cairn/K-brain/internal/netproxy"
)

// handleNetworkProxy hot-swaps the app proxy used by providers whose
// useSystemProxy toggle is on. The body is {"proxy": "<url>|direct|invalid"}
// with the same meaning as netproxy.EnvVar. The value is never echoed back
// or logged because it may carry credentials.
func (s *Server) handleNetworkProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in struct {
		Proxy *string `json:"proxy"`
	}
	if err := decodeJSON(w, r, &in); err != nil || in.Proxy == nil {
		writeJSONError(w, http.StatusBadRequest, "proxy is required")
		return
	}
	if err := netproxy.Set(*in.Proxy); err != nil {
		writeJSONError(w, http.StatusBadRequest, "proxy configuration is invalid")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}
