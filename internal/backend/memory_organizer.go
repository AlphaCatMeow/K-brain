package backend

import (
	"net/http"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/memoryruntime"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

func (s *Server) handleMemoryOrganizer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in struct {
		Action  string                         `json:"action"`
		Workdir string                         `json:"workdir"`
		RunID   string                         `json:"runId"`
		Trigger memoryruntime.OrganizerTrigger `json:"trigger"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	workdir := in.Workdir
	if workdir == "" {
		workdir = s.defaultCWD
	}
	var runtime *memoryruntime.Runtime
	if s.memoryRuntimeFactory != nil {
		var err error
		runtime, err = s.memoryRuntimeFactory(r.Context(), workdir, protocol.ModelRef{})
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		writeJSONError(w, http.StatusNotImplemented, "memory organizer runtime unavailable")
		return
	}
	defer runtime.Close()
	var result any
	var err error
	switch strings.ToLower(in.Action) {
	case "run", "organize":
		result, err = runtime.RunOrganizer(r.Context(), workdir, in.Trigger)
	case "apply":
		result, err = runtime.ApplyOrganizerRun(r.Context(), in.RunID)
	case "history":
		result, err = s.memoryStore.ListOrganizeRuns()
	default:
		writeJSONError(w, http.StatusBadRequest, "action must be run, apply, or history")
		return
	}
	if err != nil && r.Context().Err() != nil {
		writeJSONError(w, http.StatusRequestTimeout, err.Error())
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
