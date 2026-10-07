package backend

import (
	"context"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/computer/cua"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

// Status never opens an MCP desktop session or performs input/capture.
func (s *Server) handleComputer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.settings == nil {
		writeJSONError(w, http.StatusNotImplemented, "computer settings are unavailable")
		return
	}
	cfg := s.settings.Snapshot().Computer
	backend := cfg.Backend
	if backend == "" {
		backend = "cua"
	}
	policy := cfg.ApprovalPolicy
	if policy == "" {
		policy = "ask"
	}
	status := map[string]any{"backend": backend, "enabled": cfg.Enabled == nil || *cfg.Enabled, "approvalPolicy": policy, "platform": runtime.GOOS, "executionOwner": "kbrain", "installed": false}
	if backend != "cua" {
		status["message"] = "Legacy computer helper selected; Cua status is not applicable"
		writeJSON(w, http.StatusOK, status)
		return
	}
	argv, err := cua.ResolveCommand(cfg.Command)
	if err != nil {
		status["message"] = err.Error()
		writeJSON(w, http.StatusOK, status)
		return
	}
	status["installed"] = true
	status["command"] = argv
	// Custom launchers may not implement Cua's CLI probes; never execute them on GET.
	if len(cfg.Command) == 0 {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, argv[0], "--version").Output()
		if err == nil {
			status["driverVersion"] = strings.TrimSpace(string(out))
		} else {
			status["message"] = "Cua version probe failed: " + err.Error()
		}
	}
	status["permissionsVerified"] = false
	writeJSON(w, http.StatusOK, status)
}

func computerApprovalPolicy(cfg config.ComputerConfig) string {
	if cfg.ApprovalPolicy == "" {
		return "ask"
	}
	return cfg.ApprovalPolicy
}
