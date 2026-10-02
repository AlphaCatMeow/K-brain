package backend

import (
	"net/http"
	"strings"
)

func (s *Server) historySearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	in.Query = strings.TrimSpace(in.Query)
	if in.Query == "" || in.Limit < 1 || in.Limit > 200 {
		writeJSONError(w, http.StatusBadRequest, "query and limit are required")
		return
	}
	result, err := s.store.SearchHistory(r.Context(), in.Query, in.Limit)
	if err != nil {
		mutationError(w, err)
		return
	}
	matches := make([]map[string]any, 0, len(result.Matches))
	for i, match := range result.Matches {
		item := map[string]any{
			"source": "message", "conversationId": match.SessionID, "title": match.Title,
			"cwd": match.CWD, "segmentIndex": i, "segmentId": match.MessageID,
			"messageId": match.MessageID, "messageIndex": match.MessageOffset, "role": match.Role,
			"snippet": match.Snippet, "score": match.Score, "updatedAt": match.UpdatedAt.UnixMilli(),
		}
		matches = append(matches, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"matches": matches, "truncated": result.Truncated})
}
