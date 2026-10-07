package ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// CacheObservation contains hashes of the final wire payload, never prompt text or credentials.
// Matching hashes indicate reusable structure, not provider cache hits or token counts.
type CacheObservation struct {
	Protocol       string
	ParametersHash string
	MessagesHash   []string
}

type cacheObserverKey struct{}

func WithCacheObserver(ctx context.Context, observe func(CacheObservation)) context.Context {
	return context.WithValue(ctx, cacheObserverKey{}, observe)
}

func observeCacheRequest(ctx context.Context, protocol string, body []byte) {
	observe, ok := ctx.Value(cacheObserverKey{}).(func(CacheObservation))
	if !ok || observe == nil {
		return
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil {
		return
	}
	field := "messages"
	if protocol == "/responses" {
		field = "input"
	}
	messages, _ := payload[field].([]any)
	delete(payload, field)
	// Stream transport and cache hints are not part of the model-visible prefix.
	for _, key := range []string{"stream", "stream_options", "prompt_cache_key", "prompt_cache_retention", "prompt_cache_options"} {
		delete(payload, key)
	}
	removeCacheMarkers(payload)
	observation := CacheObservation{Protocol: protocol, ParametersHash: cacheHash(payload), MessagesHash: make([]string, 0, len(messages))}
	for _, message := range messages {
		removeCacheMarkers(message)
		observation.MessagesHash = append(observation.MessagesHash, cacheHash(message))
	}
	observe(observation)
}

func cacheHash(value any) string {
	data, _ := json.Marshal(value)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func removeCacheMarkers(value any) {
	switch v := value.(type) {
	case map[string]any:
		delete(v, "cache_control")
		for key, nested := range v {
			// Tool schemas and arguments can legitimately define cache_control properties.
			if key == "parameters" || key == "input_schema" || key == "input" || key == "arguments" {
				continue
			}
			removeCacheMarkers(nested)
		}
	case []any:
		for _, nested := range v {
			removeCacheMarkers(nested)
		}
	}
}

// SharedCacheMessages counts whole matching messages; it deliberately does not estimate tokens.
func SharedCacheMessages(previous, current CacheObservation) int {
	if previous.Protocol != current.Protocol || previous.ParametersHash != current.ParametersHash {
		return 0
	}
	n := min(len(previous.MessagesHash), len(current.MessagesHash))
	for i := 0; i < n; i++ {
		if previous.MessagesHash[i] != current.MessagesHash[i] {
			return i
		}
	}
	return n
}
