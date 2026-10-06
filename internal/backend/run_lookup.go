package backend

import (
	"net/http"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func (s *Server) lookupRun(w http.ResponseWriter, r *http.Request, id string) {
	requestID := r.URL.Query().Get("client_request_id")
	if strings.TrimSpace(requestID) == "" {
		writeJSONError(w, http.StatusBadRequest, "client_request_id is required")
		return
	}
	rt, err := s.historyRuntime(id)
	if err != nil {
		mutationError(w, err)
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.deleted {
		mutationError(w, session.ErrNotFound)
		return
	}
	if rt.runtimeErr != nil {
		writeJSONError(w, http.StatusInternalServerError, "session runtime is quarantined")
		return
	}
	record, ok := rt.runs[requestID]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "run acceptance not found")
		return
	}
	writeJSON(w, http.StatusOK, protocol.RunAccepted{Version: protocol.Version, ConversationID: id, RunID: record.RunID, AcceptedSeq: record.AcceptedSeq})
}
