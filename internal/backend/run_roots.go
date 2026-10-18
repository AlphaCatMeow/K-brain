package backend

import (
	"fmt"
	"net/http"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

type extendRunRootsRequest struct {
	Roots []protocol.WorkspaceRoot `json:"roots"`
}

type runRootsResponse struct {
	Version string                   `json:"version"`
	Roots   []protocol.WorkspaceRoot `json:"roots"`
}

// extendRunRoots grants additional workspace roots to an in-progress run, so a
// path the user approves mid-turn is usable by the same turn's next tool call.
// Grants are validated like run options and only ever widen access.
func (s *Server) extendRunRoots(w http.ResponseWriter, r *http.Request, id, run string) {
	var req extendRunRootsRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Roots) == 0 {
		writeJSONError(w, http.StatusBadRequest, "roots must not be empty")
		return
	}
	add := make([]tools.WorkspaceRoot, len(req.Roots))
	for i := range req.Roots {
		if err := normalizeWorkspaceRoot(&req.Roots[i], fmt.Sprintf("roots[%d]", i)); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		add[i] = tools.WorkspaceRoot{Path: req.Roots[i].Path, Access: req.Roots[i].Access}
	}
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	defer s.releaseRuntime(rt)
	rt.mu.Lock()
	active := rt.runID == run && rt.cancel != nil
	runCtx := rt.terminalContexts[run]
	rt.mu.Unlock()
	if !active {
		writeJSONError(w, http.StatusConflict, "run is not active")
		return
	}
	holder, ok := tools.RunRootsFrom(runCtx)
	if runCtx == nil || !ok {
		writeJSONError(w, http.StatusConflict, "run has no workspace roots to extend")
		return
	}
	writeJSON(w, http.StatusOK, runRootsResponse{Version: protocol.Version, Roots: protocolRoots(holder.Extend(add))})
}

func protocolRoots(roots []tools.WorkspaceRoot) []protocol.WorkspaceRoot {
	out := make([]protocol.WorkspaceRoot, len(roots))
	for i, root := range roots {
		out[i] = protocol.WorkspaceRoot{Path: root.Path, Access: root.Access}
	}
	return out
}

func toolErrorFromDenial(denial *tools.PathPolicyError) *protocol.ToolError {
	if denial == nil {
		return nil
	}
	return &protocol.ToolError{Code: denial.Code, Path: denial.Path, Access: denial.Access, Roots: protocolRoots(denial.Roots)}
}
