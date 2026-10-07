package ai

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// CacheCapabilities describes the API contract, not whether its cache will hit.
// Nil booleans retain endpoint defaults; explicit false supports strict relays.
type CacheCapabilities struct {
	SupportsPromptCacheKey *bool  `json:"supportsPromptCacheKey,omitempty"`
	SupportsLongRetention  *bool  `json:"supportsLongRetention,omitempty"`
	SessionAffinityFormat  string `json:"sessionAffinityFormat,omitempty"`
	ResponsesCacheOptions  bool   `json:"responsesCacheOptions,omitempty"`
}

func (c CacheCapabilities) Validate() error {
	switch c.SessionAffinityFormat {
	case "", "none", "openai", "openai-nosession", "openrouter":
		return nil
	default:
		return fmt.Errorf("unsupported cache sessionAffinityFormat %q", c.SessionAffinityFormat)
	}
}

// CacheEndpointHost uses the parsed hostname, never a substring of an untrusted URL.
func CacheEndpointHost(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

type cacheRequestKey struct{}

func (c *OpenAI) cacheContext(ctx context.Context, req Request) context.Context {
	key := req.PromptCacheKey
	if key == "" {
		key = c.CacheKey
	}
	return context.WithValue(ctx, cacheRequestKey{}, clampCacheKey(key))
}

func cacheControl(retention string) map[string]any {
	control := map[string]any{"type": "ephemeral"}
	if retention == "long" {
		control["ttl"] = "1h"
	}
	return control
}

func markCacheBlock(blocks []any, control map[string]any, textOnly bool) bool {
	for i := len(blocks) - 1; i >= 0; i-- {
		block, ok := blocks[i].(map[string]any)
		if !ok {
			continue
		}
		kind, _ := block["type"].(string)
		if kind == "text" {
			if text, _ := block["text"].(string); text == "" {
				continue
			}
		} else if textOnly || (kind != "image" && kind != "tool_use" && kind != "tool_result" && kind != "document") {
			continue
		}
		block["cache_control"] = control
		return true
	}
	return false
}

func markChatCacheMessage(message map[string]any, control map[string]any) bool {
	if text, ok := message["content"].(string); ok && text != "" {
		message["content"] = []any{map[string]any{"type": "text", "text": text, "cache_control": control}}
		return true
	}
	blocks, _ := message["content"].([]any)
	return markCacheBlock(blocks, control, true)
}

func (c *OpenAI) applyChatCacheControl(payload map[string]any) {
	if c.CacheControlFormat != "anthropic" || c.CacheRetention == "none" {
		return
	}
	retention := "short"
	if c.CacheRetention == "long" && c.SupportsLongCacheRetention {
		retention = "long"
	}
	control := cacheControl(retention)
	messages, _ := payload["messages"].([]any)
	// Keep earlier message representations stable as the tail marker moves forward.
	for _, value := range messages {
		if message, ok := value.(map[string]any); ok {
			if text, ok := message["content"].(string); ok && text != "" {
				message["content"] = []any{map[string]any{"type": "text", "text": text}}
			}
		}
	}
	for _, value := range messages {
		message, ok := value.(map[string]any)
		if ok && (message["role"] == "system" || message["role"] == "developer") && markChatCacheMessage(message, control) {
			break
		}
	}
	for i := len(messages) - 1; i >= 0; i-- {
		message, ok := messages[i].(map[string]any)
		if ok && (message["role"] == "user" || message["role"] == "assistant" || message["role"] == "tool") && markChatCacheMessage(message, control) {
			break
		}
	}
	if tools, ok := payload["tools"].([]any); ok && len(tools) > 0 {
		if tool, ok := tools[len(tools)-1].(map[string]any); ok {
			tool["cache_control"] = control
		}
	}
}
