package backend

import (
	"context"
	"encoding/json"
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

// computerRememberable reports whether the session-wide grant may cover a Cua
// tool. Tools that replay or batch other tools, or install software, always ask.
func computerRememberable(tool string) bool {
	switch tool {
	case "", "run_actions", "replay_trajectory", "install_ffmpeg", "install_extension":
		return false
	}
	return true
}

// computerApprovalRule classifies a computer_exec request for interactive approval.
// discover/describe/guide only read the driver catalog and bundled guides, so they
// need no prompt. A call is keyed by its Cua tool; once the user approves any call,
// every rememberable tool runs without asking for the rest of the session.
func computerApprovalRule(command string) (rule string, readOnly, rememberable bool) {
	var request struct {
		Action string `json:"action"`
		Tool   string `json:"tool"`
	}
	if json.Unmarshal([]byte(command), &request) != nil {
		return "", false, false
	}
	switch request.Action {
	case "discover", "describe", "guide":
		return request.Action, true, false
	case "call":
		tool := strings.TrimSpace(request.Tool)
		if tool == "" {
			return "", false, false
		}
		return "call:" + tool, false, computerRememberable(tool)
	}
	return "", false, false
}
