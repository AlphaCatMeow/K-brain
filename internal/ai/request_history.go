package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// prepareRequestHistory is shared by every wire adapter. Canonical stored history
// remains untouched; provider-private replay is used only on its originating route.
func prepareRequestHistory(messages []Message, api, endpoint, model string) []Message {
	out := repairToolHistory(stripAuthored(messages))
	for i := range out {
		if !replayMatches(out[i], api, endpoint, model) {
			out[i].Replay = nil
		}
	}
	return wireToolCallIDs(out, api)
}

// responsesMaxCallID is the Responses API limit for function_call.call_id.
const responsesMaxCallID = 64

// wireToolCallIDs rewrites stored tool call IDs that the target protocol rejects.
// Imported pi-ai history stores Responses calls as "call_id|item_id"; the part before
// "|" is the provider-issued call_id. Valid IDs pass through unchanged, so native
// requests stay byte-identical. Gemini is left alone: its encoder looks up thought
// signatures by the stored ID. Request observers see the stored IDs, not these.
func wireToolCallIDs(msgs []Message, api string) []Message {
	if api != APIResponses && api != APIMessages {
		return msgs
	}
	wire := map[string]string{}  // stored ID -> wire ID
	owner := map[string]string{} // wire ID -> stored ID
	mapID := func(id string) string {
		if id == "" {
			return id
		}
		if mapped, ok := wire[id]; ok {
			return mapped
		}
		mapped := id
		if prefix, _, ok := strings.Cut(id, "|"); ok {
			mapped = prefix
		}
		if !validWireToolCallID(mapped, api) {
			mapped = hashedToolCallID(id)
		}
		if prev, taken := owner[mapped]; taken && prev != id {
			mapped = hashedToolCallID(id)
		}
		wire[id], owner[mapped] = mapped, id
		return mapped
	}
	for i := range msgs {
		msgs[i].ToolCallID = mapID(msgs[i].ToolCallID)
		if len(msgs[i].ToolCalls) == 0 {
			continue
		}
		calls := make([]ToolCall, len(msgs[i].ToolCalls))
		copy(calls, msgs[i].ToolCalls)
		for j := range calls {
			calls[j].ID = mapID(calls[j].ID)
		}
		msgs[i].ToolCalls = calls
	}
	return msgs
}

func validWireToolCallID(id string, api string) bool {
	if id == "" {
		return false
	}
	if api == APIResponses {
		return len(id) <= responsesMaxCallID
	}
	// Anthropic tool_use.id: ^[a-zA-Z0-9_-]+$
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func hashedToolCallID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "call_" + hex.EncodeToString(sum[:16])
}
