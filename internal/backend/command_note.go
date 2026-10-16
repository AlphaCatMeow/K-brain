package backend

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

const maxCommandNoteBytes = 16 * 1024

// commandNoteInput records a desktop slash command that does not run the model, such as
// /loop list, as one exchange: the text the user typed and the result shown to them.
// The exchange stays in history and in the model context so later turns know about it.
type commandNoteInput struct {
	Command string `json:"command"`
	Result  string `json:"result"`
}

func commandNoteID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "msg-" + hex.EncodeToString(b[:])
}

func (s *Server) handleCommandNote(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in commandNoteInput
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	command, result := strings.TrimSpace(in.Command), strings.TrimSpace(in.Result)
	if !strings.HasPrefix(command, "/") || result == "" || len(command)+len(result) > maxCommandNoteBytes {
		writeJSONError(w, http.StatusBadRequest, "command must start with / and result is required")
		return
	}
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	defer s.releaseRuntime(rt)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.agent == nil || rt.recorder == nil {
		writeJSONError(w, http.StatusConflict, "session runtime is unavailable")
		return
	}
	// A running turn owns the message list; the client shows the result locally instead.
	if rt.activeLocked() {
		writeJSONError(w, http.StatusConflict, "session already has a running turn")
		return
	}
	now := time.Now()
	messages := append(rt.agent.MessagesSnapshot(),
		ai.Message{ID: commandNoteID(), Role: "user", Content: command, Authored: true, SentAt: &now},
		ai.Message{ID: commandNoteID(), Role: "assistant", Content: result, StopReason: ai.StopReasonStop},
	)
	previous := rt.agent.MessagesSnapshot()
	rt.agent.RestoreMessages(messages)
	if err := rt.recorder.Save(); err != nil {
		rt.agent.RestoreMessages(previous)
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": protocol.Version, "ok": true})
}
