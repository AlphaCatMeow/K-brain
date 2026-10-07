package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

var modelToolName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)

// generate invokes exactly one configured provider. Tool calls are data, never executed here.
func (s *Server) generate(w http.ResponseWriter, r *http.Request) {
	var in protocol.GenerateRequest
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	if strings.TrimSpace(in.Model.Model) == "" || !s.knownModel(in.Model) {
		writeJSONError(w, 400, "select an available configured model")
		return
	}
	if s.settings != nil {
		cfg := s.settings.Snapshot()
		if cfg == nil || !cfg.Providers[in.Model.Provider].ModelActive(in.Model.Model) {
			writeJSONError(w, 400, "selected model is disabled")
			return
		}
	}
	if len(in.Messages) == 0 || len(in.Messages) > 10000 || len(in.Tools) > 256 || in.MaxOutputTokens < 0 || in.MaxOutputTokens > 1_000_000 || len(in.CacheKey) > 4096 {
		writeJSONError(w, 400, "invalid messages, tools, output limit, or cache key")
		return
	}
	messages := make([]ai.Message, 0, len(in.Messages))
	for _, message := range in.Messages {
		for _, block := range message.Content {
			if block.Type != protocol.ContentText && block.Type != protocol.ContentThinking && block.Type != protocol.ContentImage && block.Type != protocol.ContentFile {
				writeJSONError(w, 400, "use top-level tool_calls and tool_call_id for tool history")
				return
			}
		}
		converted, err := message.ToAIMessage()
		if err != nil {
			writeJSONError(w, 400, "invalid canonical message")
			return
		}
		messages = append(messages, converted)
	}
	tools := make([]ai.Tool, 0, len(in.Tools))
	seen := map[string]bool{}
	for _, tool := range in.Tools {
		var schema map[string]json.RawMessage
		if !modelToolName.MatchString(tool.Name) || seen[tool.Name] || json.Unmarshal(tool.Parameters, &schema) != nil || schema == nil {
			writeJSONError(w, 400, "invalid or duplicate tool definition")
			return
		}
		seen[tool.Name] = true
		tools = append(tools, ai.NewTool(tool.Name, tool.Description, string(tool.Parameters)))
	}
	switch in.Reasoning {
	case "", "off", "none", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		writeJSONError(w, 400, "unsupported reasoning level")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	if s.factory == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "model generation is unavailable")
		return
	}
	ag, err := s.factory(ctx, "", in.Model)
	if err != nil {
		writeTextError(w, err)
		return
	}
	maxOutput := in.MaxOutputTokens
	if maxOutput == 0 {
		maxOutput = ag.MaxTokens
	}
	if maxOutput == 0 {
		maxOutput = 4096
	}
	if ag.MaxTokens > 0 && maxOutput > ag.MaxTokens {
		writeJSONError(w, 400, "output limit exceeds configured model limit")
		return
	}
	if !ag.Vision {
		for _, message := range messages {
			for _, part := range message.Parts {
				if part.Type == "image_url" {
					writeJSONError(w, 400, "selected model does not support images")
					return
				}
			}
		}
	}
	client := ag.Client.Clone()
	client.SetCacheKey(in.CacheKey)
	req := ai.Request{Model: ag.Model, Messages: messages, Tools: tools, MaxTokens: maxOutput, ReasoningEffort: in.Reasoning}
	var mu sync.Mutex
	var writeErr error
	emit := func(event protocol.GenerateEvent) {}
	if in.Stream {
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeJSONError(w, 500, "streaming unavailable")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		emit = func(event protocol.GenerateEvent) {
			mu.Lock()
			defer mu.Unlock()
			if writeErr != nil {
				return
			}
			event.Version = protocol.Version
			body, err := json.Marshal(event)
			if err == nil {
				_, err = fmt.Fprintf(w, "data: %s\n\n", body)
			}
			if err != nil {
				writeErr = err
				cancel()
				return
			}
			flusher.Flush()
		}
		emit(protocol.GenerateEvent{Type: "request.accepted"})
	}
	message, usage, err := client.Stream(ctx, req, func(text string) { emit(protocol.GenerateEvent{Type: "assistant.text.delta", Text: text}) }, func(text string) { emit(protocol.GenerateEvent{Type: "assistant.thinking.delta", Text: text}) }, nil)
	if err != nil {
		if in.Stream {
			emit(protocol.GenerateEvent{Type: "request.failed", Error: "model request failed"})
		} else {
			writeTextError(w, err)
		}
		return
	}
	canonical, err := protocol.FromAIMessageValidated(message)
	if err != nil || canonical.Role != protocol.RoleAssistant {
		if in.Stream {
			emit(protocol.GenerateEvent{Type: "request.failed", Error: "invalid model response"})
		} else {
			writeJSONError(w, http.StatusBadGateway, "invalid model response")
		}
		return
	}
	response := protocol.GenerateResponse{Version: protocol.Version, Model: in.Model, Message: canonical, Usage: protocol.FromAIUsage(&usage)}
	response.Message.Model, response.Message.Provider = in.Model.Model, in.Model.Provider
	response.Message.Usage = response.Usage
	if in.Stream {
		emit(protocol.GenerateEvent{Type: "request.completed", Response: &response})
	} else {
		writeJSON(w, 200, response)
	}
}
