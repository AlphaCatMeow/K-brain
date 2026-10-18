package backend

import (
	"context"
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

type rootRequestWaiter struct {
	request protocol.RootRequest
	// result receives the grants to add, or nil to deny.
	result chan []tools.WorkspaceRoot
}

// waitRootRequest publishes root.requested for a denied path and blocks the
// tool call until the client grants or denies it, or the call is cancelled.
// A grant is added to the run before returning true so the caller can re-check.
func (s *Server) waitRootRequest(rt *runtimeSession, runID string, roots *tools.RunRoots, req tools.RootRequest, ctx context.Context) bool {
	id := newRunID()
	waiter := &rootRequestWaiter{
		request: protocol.RootRequest{RequestID: id, ToolCallID: tools.ToolCallID(ctx), Tool: req.Tool, Code: req.Code, Path: req.Path, Access: req.Access, Roots: protocolRoots(req.Roots)},
		result:  make(chan []tools.WorkspaceRoot, 1),
	}
	rt.mu.Lock()
	if rt.rootRequests == nil {
		rt.rootRequests = map[string]*rootRequestWaiter{}
	}
	rt.rootRequests[id] = waiter
	rt.mu.Unlock()
	defer func() { rt.mu.Lock(); delete(rt.rootRequests, id); rt.mu.Unlock() }()
	s.publish(rt, protocol.EventRootRequested, waiter.request, "")
	select {
	case grants := <-waiter.result:
		resolution := protocol.RootResolution{RequestID: id, Decision: "deny"}
		if grants != nil {
			resolution = protocol.RootResolution{RequestID: id, Decision: "grant", Roots: protocolRoots(roots.Extend(grants))}
		}
		s.publish(rt, protocol.EventRootResolved, resolution, "")
		return grants != nil
	case <-ctx.Done():
		return false
	}
}

func (s *Server) resolveRootRequest(w http.ResponseWriter, r *http.Request, id, requestID string) {
	var req protocol.RootResolution
	if err := decodeJSON(w, r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.RequestID != "" && req.RequestID != requestID {
		writeJSONError(w, http.StatusBadRequest, "request_id does not match the path")
		return
	}
	var grants []tools.WorkspaceRoot
	switch req.Decision {
	case "deny":
	case "grant":
		if len(req.Roots) == 0 {
			writeJSONError(w, http.StatusBadRequest, "a grant needs at least one root")
			return
		}
		grants = make([]tools.WorkspaceRoot, len(req.Roots))
		for i := range req.Roots {
			if err := normalizeWorkspaceRoot(&req.Roots[i], fmt.Sprintf("roots[%d]", i)); err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			grants[i] = tools.WorkspaceRoot{Path: req.Roots[i].Path, Access: req.Roots[i].Access}
		}
	default:
		writeJSONError(w, http.StatusBadRequest, `decision must be "grant" or "deny"`)
		return
	}
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	defer s.releaseRuntime(rt)
	rt.mu.Lock()
	waiter := rt.rootRequests[requestID]
	rt.mu.Unlock()
	if waiter == nil {
		writeJSONError(w, http.StatusNotFound, "root request is not pending")
		return
	}
	select {
	case waiter.result <- grants:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeJSONError(w, http.StatusConflict, "root request was already answered")
	}
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
