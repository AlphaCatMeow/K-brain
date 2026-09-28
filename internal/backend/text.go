package backend

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// generateText is intentionally stateless. It accepts the canonical message
// shape, never exposes provider credentials, and never supplies tools upstream.
func (s *Server) generateText(w http.ResponseWriter, r *http.Request) {
	var in protocol.TextGenerateRequest
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	if strings.TrimSpace(in.Model.Model) == "" {
		writeJSONError(w, http.StatusBadRequest, "model.model is required")
		return
	}
	if !s.knownModel(in.Model) {
		writeJSONError(w, http.StatusBadRequest, "model is not available in the K-brain catalog")
		return
	}
	if len(in.Messages) == 0 {
		writeJSONError(w, http.StatusBadRequest, "messages are required")
		return
	}
	if in.Output != "" && in.Output != "text" && in.Output != "json" {
		writeJSONError(w, http.StatusBadRequest, "output must be text or json")
		return
	}
	messages := make([]ai.Message, 0, len(in.Messages))
	for _, message := range in.Messages {
		if message.Role == protocol.RoleTool || len(message.ToolCalls) > 0 || message.ToolCallID != "" {
			writeJSONError(w, http.StatusBadRequest, "messages must not contain tools")
			return
		}
		if message.Role != protocol.RoleSystem && message.Role != protocol.RoleDeveloper && message.Role != protocol.RoleUser && message.Role != protocol.RoleAssistant {
			writeJSONError(w, http.StatusBadRequest, "messages must use system, developer, user, or assistant roles")
			return
		}
		for _, block := range message.Content {
			if block.Type != protocol.ContentText {
				writeJSONError(w, http.StatusBadRequest, "messages must contain text blocks only")
				return
			}
		}
		converted, err := message.ToAIMessage()
		if err != nil || strings.TrimSpace(converted.TextContent()) == "" {
			writeJSONError(w, http.StatusBadRequest, "messages must contain text")
			return
		}
		messages = append(messages, converted)
	}

	client, model, err := s.textClient(r.Context(), in.Model)
	if err != nil {
		writeTextError(w, err)
		return
	}
	request := ai.Request{Model: model, Messages: messages, MaxTokens: 4096, Stream: false}
	text, usage, err := client.Complete(r.Context(), request)
	if err != nil {
		writeTextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.TextGenerateResponse{
		Version: protocol.Version,
		Text:    text,
		Model:   in.Model,
		Usage:   protocol.FromAIUsage(&usage),
	})
}

func (s *Server) knownModel(selected protocol.ModelRef) bool {
	if s.settings != nil {
		cfg := s.settings.Snapshot()
		if cfg == nil {
			return false
		}
		if _, ok := cfg.Providers[selected.Provider]; !ok {
			return false
		}
		model, ok := cfg.Models[selected.Model]
		if !ok {
			return false
		}
		for _, provider := range model.Providers {
			if provider == selected.Provider {
				return true
			}
		}
		return false
	}
	for _, candidate := range s.models {
		if candidate.Provider == selected.Provider && candidate.Model == selected.Model {
			return true
		}
	}
	return len(s.models) == 0
}

func (s *Server) textClient(ctx context.Context, selected protocol.ModelRef) (ai.Client, string, error) {
	if s.factory == nil {
		return nil, "", errors.New("text generation is unavailable")
	}
	// The factory is the only backend route to provider credentials. The agent
	// is not run: its client is used for a request with an explicit empty Tools.
	ag, err := s.factory(ctx, "", selected)
	if err != nil {
		return nil, "", err
	}
	return ag.Client, ag.Model, nil
}

func writeTextError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		writeJSONError(w, http.StatusRequestTimeout, "text generation cancelled")
		return
	}
	// Provider errors are deliberately collapsed at this boundary. Their text
	// may contain upstream URLs, request headers, or credential fragments.
	writeJSONError(w, http.StatusBadGateway, "text generation failed")
}
