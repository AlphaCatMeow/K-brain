package ai

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRequestHistoryScopesReplayWithoutChangingStoredHistory(t *testing.T) {
	for _, source := range []string{APIChatCompletions, APIResponses, APIMessages, APIGemini} {
		messages := []Message{{Role: "user", Content: "question"}, {Role: "assistant", Content: "answer", Replay: &ProviderReplay{API: source, Endpoint: "https://origin.test", Model: "m", Blocks: []json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":"private"}`)}}}}
		before, _ := json.Marshal(messages)
		for _, target := range []string{APIChatCompletions, APIResponses, APIMessages, APIGemini} {
			out := prepareRequestHistory(messages, target, "https://origin.test", "m")
			if (out[1].Replay != nil) != (target == source) {
				t.Fatalf("replay crossed protocol %s -> %s", source, target)
			}
			if prepareRequestHistory(messages, target, "https://other.test", "m")[1].Replay != nil || prepareRequestHistory(messages, target, "https://origin.test", "other")[1].Replay != nil {
				t.Fatal("replay crossed endpoint/model")
			}
		}
		after, _ := json.Marshal(messages)
		if string(before) != string(after) {
			t.Fatal("stored history mutated")
		}
	}
}

func TestLateDuplicateToolResultsAreContextNotUnpairedCalls(t *testing.T) {
	call := ToolCall{ID: "call"}
	call.Function.Name = "lookup"
	messages := []Message{{Role: "assistant", ToolCalls: []ToolCall{call}}, {Role: "tool", ToolCallID: "call", Content: "first"}, {Role: "user", Content: "next"}, {Role: "tool", ToolCallID: "call", Content: "late"}}
	out := repairToolHistory(messages)
	if out[1].Name != "lookup" || out[3].Role != "user" || out[3].ToolCallID != "" {
		t.Fatalf("invalid repaired history: %+v", out)
	}
	if !reflect.DeepEqual(out, repairToolHistory(out)) {
		t.Fatal("repair must be idempotent")
	}
	missing := repairToolHistory([]Message{{Role: "assistant", ToolCalls: []ToolCall{call}}, {Role: "user", Content: "interrupted"}, {Role: "tool", ToolCallID: "call", Content: "late"}})
	if len(missing) != 4 || missing[1].Role != "tool" || missing[3].Role != "user" {
		t.Fatalf("missing synthetic result: %+v", missing)
	}
}
