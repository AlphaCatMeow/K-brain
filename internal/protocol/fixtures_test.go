package protocol

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestCanonicalMessageFixtureUsesProviderNeutralReplay(t *testing.T) {
	raw, err := os.ReadFile("testdata/canonical_messages.json")
	if err != nil {
		t.Fatal(err)
	}
	var messages []Message
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatalf("fixture messages = %d", len(messages))
	}
	for i, message := range messages {
		converted, err := message.ToAIMessage()
		if err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
		replay, err := FromAIMessageValidated(converted)
		if err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
		expected := message
		if !reflect.DeepEqual(replay, expected) {
			t.Fatalf("replay %d = %+v, want %+v", i, replay, expected)
		}
	}
}

func TestCanonicalEventFixtureValidatesAndPreservesToolIdentity(t *testing.T) {
	raw, err := os.ReadFile("testdata/canonical_events.json")
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("fixture events = %d", len(events))
	}
	for i, event := range events {
		if err := event.Validate(); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if event.Seq != int64(i+1) {
			t.Fatalf("non-contiguous sequence: %+v", event)
		}
	}
	var payload ToolCallEvent
	if err := json.Unmarshal(events[1].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if events[1].Type != EventToolCall || payload.ToolCall.ID != "call-1" || payload.ToolCall.Name != "bash" {
		t.Fatalf("tool event = %+v / %+v", events[1], payload)
	}
	if err := (Message{Role: RoleAssistant, ToolCalls: []ToolCall{payload.ToolCall}}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalMetadataSurvivesPersistenceRoundTrip(t *testing.T) {
	sentAt := time.Date(2026, time.September, 28, 0, 0, 0, 0, time.UTC)
	message := Message{
		Role: RoleAssistant, Model: "model-v1", Provider: "provider-route",
		Content:      []ContentBlock{{Type: ContentText, Text: "answer"}},
		Usage:        &Usage{InputTokens: 13, OutputTokens: 5, CachedTokens: 3, CacheWriteTokens: 2},
		HostedSearch: []HostedSearch{{Type: "web_search", ID: "search-1", Provider: "gemini", Status: "completed", Queries: []string{"reference"}, Sources: []HostedSearchSource{{URL: "https://example.com/reference", Title: "Reference", SourceType: "web"}}}},
		StopReason:   "stop", CreatedAt: &sentAt,
	}
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var persisted Message
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	converted, err := persisted.ToAIMessage()
	if err != nil {
		t.Fatal(err)
	}
	if converted.Model != "model-v1 @ provider-route" {
		t.Fatalf("model identity = %q", converted.Model)
	}
	restored, err := FromAIMessageValidated(converted)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, message) {
		t.Fatalf("restored = %+v, want %+v", restored, message)
	}
}

func TestCanonicalMessagesRejectMalformedProviderHistory(t *testing.T) {
	for name, message := range map[string]Message{
		"unknown role":       {Role: "model"},
		"orphan tool result": {Role: RoleTool},
		"user tool call":     {Role: RoleUser, ToolCalls: []ToolCall{{ID: "c", Name: "bash", Arguments: json.RawMessage(`{}`)}}},
		"array arguments":    {Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c", Name: "bash", Arguments: json.RawMessage(`[]`)}}},
		"null arguments":     {Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c", Name: "bash", Arguments: json.RawMessage(`null`)}}},
		"duplicate call":     {Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c", Name: "bash", Arguments: json.RawMessage(`{}`)}, {ID: "c", Name: "bash", Arguments: json.RawMessage(`{}`)}}},
		"missing image":      {Role: RoleUser, Content: []ContentBlock{{Type: ContentImage}}},
		"provider block":     {Role: RoleUser, Content: []ContentBlock{{Type: "input_audio"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := message.ToAIMessage(); err == nil {
				t.Fatal("malformed history accepted")
			}
		})
	}
}
