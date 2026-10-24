package backend

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/memoryruntime"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

type memoryOrganizerRequest struct {
	Action  string                         `json:"action"`
	Workdir string                         `json:"workdir"`
	RunID   string                         `json:"runId"`
	Trigger memoryruntime.OrganizerTrigger `json:"trigger"`
}

func (s *Server) handleMemoryOrganizer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in memoryOrganizerRequest
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	if s.memoryRuntimeFactory == nil {
		writeJSONError(w, http.StatusNotImplemented, "memory organizer runtime unavailable")
		return
	}
	result, err := s.executeMemoryOrganizer(r.Context(), in)
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

func (s *Server) executeMemoryOrganizer(ctx context.Context, in memoryOrganizerRequest) (any, error) {
	// UI requests are manual; scheduled auto-apply belongs to the backend scheduler.
	if in.Trigger != "" && in.Trigger != memoryruntime.OrganizerManual {
		return nil, fmt.Errorf("HTTP memory organization requires a manual trigger")
	}
	action := strings.ToLower(in.Action)
	if action != "run" && action != "organize" && action != "apply" && action != "history" {
		return nil, fmt.Errorf("action must be run, apply, or history")
	}
	if action == "history" {
		return s.memoryStore.ListOrganizeRuns()
	}
	workdir := in.Workdir
	if workdir == "" {
		workdir = s.defaultCWD
	}
	var runtime *memoryruntime.Runtime
	if s.memoryRuntimeFactory != nil {
		var err error
		runtime, err = s.memoryRuntimeFactory(ctx, workdir, protocol.ModelRef{})
		if err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("memory organizer runtime unavailable")
	}
	defer runtime.Close()
	switch action {
	case "run", "organize":
		return runtime.RunOrganizer(ctx, workdir, memoryruntime.OrganizerManual)
	case "apply":
		return runtime.ApplyOrganizerRun(ctx, in.RunID)
	}
	return nil, fmt.Errorf("unknown memory organizer action")
}
