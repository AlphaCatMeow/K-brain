package backend

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/memory"
)

func (s *Server) handleMemory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in struct {
		Command string          `json:"command"`
		Args    json.RawMessage `json:"args"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	if strings.TrimSpace(in.Command) == "" {
		writeJSONError(w, http.StatusBadRequest, "command is required")
		return
	}
	if in.Command == "chat_history_search" {
		var args struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(in.Args, &args); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		args.Query = strings.TrimSpace(args.Query)
		if args.Query == "" || args.Limit < 1 || args.Limit > 200 {
			writeJSONError(w, http.StatusBadRequest, "query and limit are required")
			return
		}
		result, err := s.store.SearchHistory(r.Context(), args.Query, args.Limit)
		if err != nil {
			mutationError(w, err)
			return
		}
		matches := make([]map[string]any, 0, len(result.Matches))
		for i, match := range result.Matches {
			matches = append(matches, map[string]any{"source": "message", "conversationId": match.SessionID, "title": match.Title, "cwd": match.CWD, "segmentIndex": i, "segmentId": match.MessageID, "messageId": match.MessageID, "messageIndex": match.MessageOffset, "role": match.Role, "snippet": match.Snippet, "score": match.Score, "updatedAt": match.UpdatedAt.UnixMilli()})
		}
		writeJSON(w, http.StatusOK, map[string]any{"matches": matches, "truncated": result.Truncated})
		return
	}
	if s.memoryStore == nil {
		writeJSONError(w, http.StatusInternalServerError, "memory store unavailable")
		return
	}
	result, err := s.memoryStore.Dispatch(in.Command, in.Args)
	if err != nil {
		status := http.StatusBadRequest
		var se *memory.StoreError
		if errors.As(err, &se) && se.Code == "not_found" {
			status = http.StatusNotFound
		}
		writeJSONError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
