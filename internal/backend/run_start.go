package backend

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// startCanonicalRun serializes acceptance and starts the shared session executor.
// ctx owns the run lifetime; HTTP callers deliberately detach from the connection.
func (s *Server) startCanonicalRun(ctx context.Context, rt *runtimeSession, in protocol.PromptRequest) (protocol.RunAccepted, int, error) {
	fail := func(status int, message string) (protocol.RunAccepted, int, error) {
		return protocol.RunAccepted{}, status, errors.New(message)
	}
	if err := ctx.Err(); err != nil {
		return protocol.RunAccepted{}, http.StatusConflict, err
	}
	if strings.TrimSpace(in.ClientRequestID) == "" {
		return fail(400, "client_request_id is required")
	}
	if in.ConversationID != "" && in.ConversationID != rt.id {
		return fail(400, "conversation_id does not match session")
	}
	if in.HookPolicy != "" && in.HookPolicy != "backend" {
		return fail(400, "hook_policy must be backend")
	}
	if len(in.HookScopeID) > 256 {
		return fail(400, "hook_scope_id is too long")
	}
	if in.StopRequested {
		return fail(409, "run was stopped before acceptance")
	}
	if strings.TrimSpace(in.Prompt) == "" && len(in.Content) == 0 {
		return fail(400, "prompt or content is required")
	}
	user := protocol.Message{Role: protocol.RoleUser}
	if in.Prompt != "" {
		user.Content = append(user.Content, protocol.ContentBlock{Type: protocol.ContentText, Text: in.Prompt})
	}
	user.Content = append(user.Content, in.Content...)
	if err := user.Validate(); err != nil {
		return fail(400, err.Error())
	}
	for _, block := range user.Content {
		if block.Type == protocol.ContentThinking {
			return fail(400, "user content must be text or image")
		}
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.closing {
		return fail(http.StatusServiceUnavailable, "backend is closing")
	}
	if rt.deleted {
		return fail(404, "session not found")
	}
	if rt.runtimeErr != nil {
		return fail(500, "session runtime is quarantined: "+rt.runtimeErr.Error())
	}
	requestHash := hashPrompt(in)
	if old, ok := rt.runs[in.ClientRequestID]; ok {
		matches := old.Kind != "compact" && old.RequestHash != "" && old.RequestHash == requestHash
		if !matches && old.Kind != "compact" {
			options, err := normalizeRunOptions(in.Options, rt.agent.WorkingDir, rt.agent.AvailableTools())
			if err == nil {
				normalized := in
				normalized.Options = &options
				matches = old.PromptHash == hashPrompt(normalized)
			}
		}
		if !matches {
			return fail(409, "client_request_id was already used with a different prompt or options")
		}
		return protocol.RunAccepted{Version: protocol.Version, ConversationID: rt.id, RunID: old.RunID, AcceptedSeq: old.AcceptedSeq}, http.StatusOK, nil
	}
	if rt.activeLocked() {
		return fail(409, "session already has a running turn")
	}
	if in.Model != nil && (in.Model.Model != "" || in.Model.Provider != "") {
		if err := s.switchModelLocked(rt, *in.Model); err != nil {
			return fail(400, "model switch failed: "+err.Error())
		}
	}
	if err := s.refreshSettingsLocked(rt); err != nil {
		return fail(400, err.Error())
	}
	// Normalize after model selection so model-specific tools and MCP filters are current.
	options, err := normalizeRunOptions(in.Options, rt.agent.WorkingDir, rt.agent.AvailableTools())
	if err != nil {
		return fail(400, err.Error())
	}
	in.Options = &options
	hash := hashPrompt(in)
	if s.mcp != nil {
		mcpTools, filter := s.mcp.ToolsForTurn(ctx, rt.agent.WorkingDir, options.MCPServerIDs, rt.mcpActivation)
		rt.agent.SetMCPTools(mcpTools)
		rt.agent.RequestToolFilter = filter
	}
	if in.ResumeMessageID != "" {
		messages := rt.agent.MessagesSnapshot()
		submitted, _ := user.ToAIMessage()
		if len(messages) == 0 || messages[len(messages)-1].Role != "user" || messages[len(messages)-1].ID != in.ResumeMessageID || !sameUserContent(messages[len(messages)-1], submitted) {
			return fail(409, "resume_message_id and content must match the tail user message")
		}
		user = protocol.FromAIMessage(messages[len(messages)-1])
	}
	if in.TurnID != "" {
		user.ID = in.TurnID
	}
	runID := newRunID()
	runCtx, cancel := context.WithCancel(ctx)
	rt.runID, rt.runDone, rt.cancel = runID, false, cancel
	rt.checkpoint = newCheckpointCapture(s.eventDir, rt.id, runID, in.TurnID)
	abort := func(err error) (protocol.RunAccepted, int, error) {
		cancel()
		rt.cancel, rt.checkpoint, rt.runDone = nil, nil, true
		rt.runtimeErr = fmt.Errorf("persist run acceptance: %w", err)
		return protocol.RunAccepted{}, 500, rt.runtimeErr
	}
	if err := rt.publishEventLocked(runID, protocol.EventRunAccepted, map[string]any{"client_request_id": in.ClientRequestID}); err != nil {
		return abort(err)
	}
	acceptedSeq := rt.nextSeq
	if in.ResumeMessageID == "" {
		if err := rt.publishEventLocked(runID, protocol.EventUserMessage, user); err != nil {
			return abort(err)
		}
	}
	rt.runs[in.ClientRequestID] = runRecord{ClientRequestID: in.ClientRequestID, PromptHash: hash, RequestHash: requestHash, RunID: runID, AcceptedSeq: acceptedSeq}
	if err := rt.persistRuns(s.eventDir); err != nil {
		return abort(err)
	}
	rt.runWorkers.Add(1)
	go func() {
		defer rt.runWorkers.Done()
		s.executeRun(rt, runCtx, runID, in, options)
	}()
	return protocol.RunAccepted{Version: protocol.Version, ConversationID: rt.id, RunID: runID, AcceptedSeq: acceptedSeq}, http.StatusAccepted, nil
}
