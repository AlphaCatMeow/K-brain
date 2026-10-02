package backend

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

type terminalStartRequest struct {
	ConversationID string `json:"conversation_id"`
	RunID          string `json:"run_id"`
	Command        string `json:"command"`
	CWD            string `json:"cwd"`
}
type terminalStartResponse struct {
	SessionID      string `json:"session_id"`
	ConversationID string `json:"conversation_id"`
	RunID          string `json:"run_id"`
}
type terminalReadRequest struct {
	ConversationID string `json:"conversation_id"`
	RunID          string `json:"run_id"`
	SessionID      string `json:"session_id"`
	MaxBytes       int    `json:"max_bytes"`
}
type terminalReadResponse struct {
	SessionID      string `json:"session_id"`
	ConversationID string `json:"conversation_id"`
	RunID          string `json:"run_id"`
	Output         string `json:"output"`
}

func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, 405, "method not allowed")
		return
	}
	var in tools.TerminalRequest
	switch r.URL.Path {
	case "/v1/terminal/sessions":
		var start terminalStartRequest
		if decodeJSON(w, r, &start) != nil {
			return
		}
		in = tools.TerminalRequest{Action: "start", ConversationID: start.ConversationID, RunID: start.RunID, Data: start.Command, CWD: start.CWD}
	case "/v1/terminal/read":
		var read terminalReadRequest
		if decodeJSON(w, r, &read) != nil {
			return
		}
		in = tools.TerminalRequest{Action: "read", ConversationID: read.ConversationID, RunID: read.RunID, SessionID: read.SessionID, MaxBytes: read.MaxBytes}
	case "/v1/terminal":
		if decodeJSON(w, r, &in) != nil {
			return
		}
	default:
		writeJSONError(w, 404, "terminal route not found")
		return
	}
	in.ConversationID, in.RunID = strings.TrimSpace(in.ConversationID), strings.TrimSpace(in.RunID)
	rt, base, err := s.canonicalHostContext(in.ConversationID, in.RunID)
	if err != nil {
		writeTerminalAuthError(w, err)
		return
	}
	action := strings.ToLower(strings.TrimSpace(in.Action))
	if (action == "start" || action == "create" || action == "input" || action == "resize") && base.Err() != nil {
		writeJSONError(w, 409, "terminal mutation requires an active canonical run")
		return
	}
	rt.mu.Lock()
	manager := rt.terminalManager
	rt.mu.Unlock()
	if manager == nil {
		writeJSONError(w, 503, "terminal manager unavailable")
		return
	}
	// Children inherit the run lifetime; approval also observes the HTTP connection.
	approvalCtx, cancel := context.WithCancel(context.WithoutCancel(base))
	defer cancel()
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()
	if base.Err() == nil {
		stopRun := context.AfterFunc(base, cancel)
		defer stopRun()
	}
	ctx := approvalCtx
	if action == "start" || action == "create" {
		ctx = tools.WithGate(base, func(req tools.GateRequest) (tools.GateDecision, string) {
			if err := tools.Authorize(approvalCtx, req.Tool, req.Command); err != nil {
				return tools.GateReject, err.Error()
			}
			return tools.GateAllowOnce, ""
		})
	}

	response, err := manager.Handle(ctx, in)
	if err != nil {
		writeTerminalAuthError(w, err)
		return
	}
	switch r.URL.Path {
	case "/v1/terminal/sessions":
		writeJSON(w, http.StatusCreated, terminalStartResponse{SessionID: response.Session.ID, ConversationID: in.ConversationID, RunID: in.RunID})
	case "/v1/terminal/read":
		writeJSON(w, http.StatusOK, terminalReadResponse{SessionID: in.SessionID, ConversationID: in.ConversationID, RunID: in.RunID, Output: string(response.Output)})
	default:
		writeJSON(w, http.StatusOK, response)
	}
}

func (s *Server) canonicalHostContext(conversationID, runID string) (*runtimeSession, context.Context, error) {
	if conversationID == "" || runID == "" {
		return nil, nil, errors.New("conversation_id and run_id are required")
	}
	rt, err := s.loadRuntimeByID(conversationID)
	if err != nil {
		return nil, nil, err
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.deleted {
		return nil, nil, errors.New("session not found")
	}
	accepted := false
	for _, record := range rt.runs {
		if record.RunID == runID {
			accepted = true
			break
		}
	}
	if !accepted {
		return nil, nil, errors.New("run_id is not an accepted canonical run")
	}
	ctx := rt.terminalContexts[runID]
	if ctx == nil {
		return nil, nil, errors.New("canonical run host context is unavailable")
	}
	return rt, ctx, nil
}

func writeTerminalAuthError(w http.ResponseWriter, err error) {
	status := http.StatusForbidden
	if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "invalid") {
		status = http.StatusBadRequest
	}
	if strings.Contains(err.Error(), "not found") {
		status = http.StatusNotFound
	}
	writeJSONError(w, status, err.Error())
}
