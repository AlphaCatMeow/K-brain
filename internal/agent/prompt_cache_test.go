package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/prompts"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

type cacheMemoryFixture struct{ block string }

func (m *cacheMemoryFixture) Inject(context.Context, string) (string, error) { return m.block, nil }
func (*cacheMemoryFixture) Completed(context.Context, MemoryTurn)            {}
func (*cacheMemoryFixture) Tools(func() string) []tools.Tool                 { return nil }
func (*cacheMemoryFixture) Close() error                                     { return nil }

func TestTurnTimesPreserveHistoryAndReloadPrefix(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 13, 14, 0, time.FixedZone("custom", 8*3600))
	messages := []ai.Message{
		{Role: "system", Content: "stable"},
		{Role: "user", Content: "question", Authored: true, SentAt: &now, Parts: []ai.ContentPart{{Type: "text", Text: "attachment"}}},
		{Role: "user", Content: "legacy"},
		{Role: "tool", Content: "result", SentAt: &now},
	}
	before, _ := json.Marshal(messages)
	first := withTurnTimes(messages)
	if !strings.HasPrefix(first[1].Content, "<kbrain-turn-time>2026-10-07T04:13:14Z</kbrain-turn-time>\n\nquestion") {
		t.Fatalf("timestamp = %q", first[1].Content)
	}
	if !reflect.DeepEqual(first[1].Parts, messages[1].Parts) || first[2].Content != "legacy" || first[3].Content != "result" {
		t.Fatal("attachments or untimestamped history changed")
	}
	after, _ := json.Marshal(messages)
	if string(before) != string(after) {
		t.Fatal("request rendering mutated stored history")
	}
	var restored []ai.Message
	if err := json.Unmarshal(before, &restored); err != nil {
		t.Fatal(err)
	}
	utc := restored[1].SentAt.UTC()
	restored[1].SentAt = &utc
	if withTurnTimes(restored)[1].Content != first[1].Content {
		t.Fatal("reload or timezone changed the model-visible prefix")
	}
}

func TestAuthoredTurnsKeepProviderPrefixAcrossReload(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	workspace := t.TempDir()
	var requests []ai.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ai.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		requests = append(requests, request)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	newAgent := func() *Agent {
		resolve := func() (string, error) { return prompts.BuildStable(workspace), nil }
		prompt, _ := resolve()
		a := New(ai.New(server.URL, "fixture"), "fixture", 100, prompt, WithSystemPromptResolver(resolve), WithTurnTimeContext())
		a.Tools = nil
		return a
	}
	a := newAgent()
	if _, err := a.TurnAuthored(t.Context(), "first", Events{}); err != nil {
		t.Fatal(err)
	}
	persisted, err := json.Marshal(a.MessagesSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	var restored []ai.Message
	if err := json.Unmarshal(persisted, &restored); err != nil {
		t.Fatal(err)
	}
	a = newAgent()
	a.RestoreMessages(restored)
	if _, err := a.TurnAuthored(t.Context(), "second", Events{}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d", len(requests))
	}
	first, _ := json.Marshal(requests[0].Messages)
	second, _ := json.Marshal(requests[1].Messages[:len(requests[0].Messages)])
	if string(first) != string(second) {
		t.Fatal("follow-up after reload changed the previous request prefix")
	}
	if !strings.Contains(requests[0].Messages[1].Content, "<kbrain-turn-time>") {
		t.Fatal("authored message lost its timestamp")
	}
	if strings.Contains(a.MessagesSnapshot()[1].Content, "<kbrain-turn-time>") {
		t.Fatal("timestamp leaked into displayed history")
	}
}

func TestContinueUserKeepsPersistedTimeInObserverAndRequest(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ai.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if !strings.Contains(request.Messages[1].Content, "2026-01-02T03:04:05Z") {
			t.Error("resume replaced the persisted timestamp")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	a := New(ai.New(server.URL, "fixture"), "fixture", 100, "stable", WithTurnTimeContext())
	a.Tools = nil
	a.Messages = append(a.Messages, ai.Message{ID: "pending", Role: "user", Content: "question", Authored: true, SentAt: &now})
	observed := false
	ctx := WithUserMessageObserver(t.Context(), func(message ai.Message) error {
		observed = true
		if message.SentAt == nil || !message.SentAt.Equal(now) || message.Content != "question" {
			t.Error("observer lost the persisted user message")
		}
		return nil
	})
	if _, err := a.ContinueUser(ctx, "pending", Events{}); err != nil {
		t.Fatal(err)
	}
	if !observed || len(a.MessagesSnapshot()) != 3 {
		t.Fatal("resume duplicated or failed to observe the user message")
	}
}

func TestPlanSnapshotPreservesPrefixAcrossChangesAndReload(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	var requests []ai.Request
	server := textServer(t, func(_ int, request ai.Request) string {
		requests = append(requests, request)
		return "done"
	})
	defer server.Close()
	newAgent := func() *Agent {
		return New(ai.New(server.URL, "fixture"), "fixture", 100, "stable", WithTurnTimeContext())
	}
	a := newAgent()
	callTodowrite(t, a, `{"todos":[{"content":"first step","status":"in_progress"}]}`)
	if _, err := a.TurnAuthored(t.Context(), "first", Events{}); err != nil {
		t.Fatal(err)
	}
	persisted, err := json.Marshal(a.MessagesSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	var restored []ai.Message
	if err := json.Unmarshal(persisted, &restored); err != nil {
		t.Fatal(err)
	}
	if restored[1].Content != "first" || !strings.Contains(restored[1].PromptContext, "first step") {
		t.Fatal("snapshot must persist separately from displayed content")
	}
	a = newAgent()
	a.RestoreMessages(restored)
	callTodowrite(t, a, `{"todos":[{"content":"second step","status":"in_progress"}]}`)
	if _, err := a.TurnAuthored(t.Context(), "second", Events{}); err != nil {
		t.Fatal(err)
	}
	callTodowrite(t, a, `{"todos":[]}`)
	if _, err := a.TurnAuthored(t.Context(), "third", Events{}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(requests); i++ {
		previous := requests[i-1].Messages
		if !reflect.DeepEqual(previous, requests[i].Messages[:len(previous)]) {
			t.Fatalf("plan update rewrote request %d prefix", i+1)
		}
	}
	if !strings.Contains(requests[1].Messages[len(requests[1].Messages)-1].Content, "second step") {
		t.Fatal("new plan was not delivered")
	}
	if strings.Contains(requests[2].Messages[len(requests[2].Messages)-1].Content, "current plan") {
		t.Fatal("cleared plan attached to new input")
	}
}

func TestPromptContextProjectionPreservesAttachmentsAndRetry(t *testing.T) {
	messages := []ai.Message{{ID: "pending", Role: "user", Content: "question", PromptContext: "persisted plan",
		Parts: []ai.ContentPart{{Type: "text", Text: "attachment"}}}}
	before, _ := json.Marshal(messages)
	projected := withPromptContext(messages)
	if projected[0].PromptContext != "" || !strings.Contains(projected[0].Content, "persisted plan") || !reflect.DeepEqual(projected[0].Parts, messages[0].Parts) {
		t.Fatal("projection lost snapshot or attachments")
	}
	if !reflect.DeepEqual(projected, withPromptContext(projected)) {
		t.Fatal("retry duplicated rendered context")
	}
	after, _ := json.Marshal(messages)
	if string(before) != string(after) {
		t.Fatal("projection mutated stored messages")
	}
	var restored []ai.Message
	if err := json.Unmarshal(before, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projected, withPromptContext(restored)) {
		t.Fatal("attachment serialization changed request prefix")
	}
	server := textServer(t, func(_ int, request ai.Request) string {
		if !strings.Contains(request.Messages[1].Content, "persisted plan") || strings.Contains(request.Messages[1].Content, "new plan") {
			t.Error("resume recomputed the saved snapshot")
		}
		return "done"
	})
	defer server.Close()
	a := New(ai.New(server.URL, "fixture"), "fixture", 100, "stable")
	a.RestoreMessages(append([]ai.Message{{Role: "system", Content: "stable"}}, restored...))
	callTodowrite(t, a, `{"todos":[{"content":"new plan","status":"pending"}]}`)
	if _, err := a.ContinueUser(t.Context(), "pending", Events{}); err != nil {
		t.Fatal(err)
	}
}

func TestTodoToolRoundsKeepRequestPrefixAndAvoidDuplicateSnapshot(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	var requests []ai.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ai.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		requests = append(requests, request)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(requests) == 1 {
			call := ai.ToolCall{ID: "plan-1", Type: "function"}
			call.Function.Name = "todowrite"
			call.Function.Arguments = `{"todos":[{"content":"inspect code","status":"in_progress"}]}`
			payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{call}}, "finish_reason": "tool_calls"}}})
			fmt.Fprintf(w, "data: %s\n\n", payload)
		} else {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	a := New(ai.New(server.URL, "fixture"), "fixture", 100, "stable")
	for _, prompt := range []string{"make a plan", "continue"} {
		if _, err := a.TurnAuthored(t.Context(), prompt, Events{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(requests) != 3 {
		t.Fatalf("requests = %d", len(requests))
	}
	for i := 1; i < len(requests); i++ {
		previous := requests[i-1].Messages
		if !reflect.DeepEqual(previous, requests[i].Messages[:len(previous)]) {
			t.Fatalf("tool round %d changed its prefix", i+1)
		}
		if !reflect.DeepEqual(requests[0].Tools, requests[i].Tools) {
			t.Fatal("tool definitions changed")
		}
	}
	last := requests[2].Messages
	if last[len(last)-1].Content != "continue" {
		t.Fatal("plan already present in tool result was redundantly injected")
	}
	if result := requests[1].Messages[3]; result.Role != "tool" || !strings.Contains(result.Content, "inspect code") {
		t.Fatal("new plan not persisted with tool result")
	}
	a.RestoreMessages([]ai.Message{{Role: "system", Content: "stable"}})
	if !strings.Contains(a.todoContext(), "inspect code") {
		t.Fatal("compaction or reset lost the current plan")
	}
}

func TestRuntimeMemoryChangesAppendWithoutRewritingPrefix(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	var requests []ai.Request
	server := textServer(t, func(_ int, request ai.Request) string {
		requests = append(requests, request)
		return "done"
	})
	defer server.Close()
	memory := &cacheMemoryFixture{}
	newAgent := func() *Agent {
		a := New(ai.New(server.URL, "fixture"), "fixture", 100, "stable",
			WithSystemPromptResolver(func() (string, error) { return "stable", nil }))
		a.SetMemoryRuntime(memory)
		return a
	}
	a := newAgent()
	for i, block := range []string{"memory one", "memory one", "memory two", ""} {
		memory.block = block
		if _, err := a.TurnAuthored(t.Context(), fmt.Sprintf("turn %d", i), Events{}); err != nil {
			t.Fatal(err)
		}
		stored, err := json.Marshal(a.MessagesSnapshot())
		if err != nil {
			t.Fatal(err)
		}
		var restored []ai.Message
		if err := json.Unmarshal(stored, &restored); err != nil {
			t.Fatal(err)
		}
		a = newAgent()
		a.RestoreMessages(restored)
	}
	for i, request := range requests {
		if request.Messages[0].Content != "stable" {
			t.Fatal("memory changed the leading system prompt")
		}
		if i > 0 && !reflect.DeepEqual(requests[i-1].Messages, request.Messages[:len(requests[i-1].Messages)]) {
			t.Fatalf("memory refresh changed request %d prefix", i+1)
		}
	}
	tail := func(i int) string { messages := requests[i].Messages; return messages[len(messages)-1].Content }
	if !strings.Contains(tail(0), "memory one") || tail(1) != "turn 1" || !strings.Contains(tail(2), "memory two") || !strings.Contains(tail(3), "No current memory entries") {
		t.Fatal("memory updates, removals or unchanged-snapshot deduplication failed")
	}
}

func TestPromptSnapshotsKeepProviderSerializedPrefix(t *testing.T) {
	for _, api := range []string{"responses", "anthropic"} {
		t.Run(api, func(t *testing.T) {
			t.Setenv("LIVEAGENT_HOME", t.TempDir())
			var bodies []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				bodies = append(bodies, body)
				w.Header().Set("Content-Type", "text/event-stream")
				if api == "responses" {
					fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
				} else {
					fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{}}\n\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"ok\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
				}
			}))
			defer server.Close()
			var client ai.Client = ai.NewResponses(server.URL, "fixture")
			field := "input"
			if api == "anthropic" {
				client = ai.NewAnthropic(server.URL, "fixture")
				field = "messages"
			}
			memory := &cacheMemoryFixture{block: "first memory"}
			a := New(client, "fixture", 100, "stable")
			a.SetMemoryRuntime(memory)
			callTodowrite(t, a, `{"todos":[{"content":"first plan","status":"pending"}]}`)
			if _, err := a.TurnAuthored(t.Context(), "first", Events{}); err != nil {
				t.Fatal(err)
			}
			memory.block = "second memory"
			callTodowrite(t, a, `{"todos":[{"content":"second plan","status":"in_progress"}]}`)
			if _, err := a.TurnAuthored(t.Context(), "second", Events{}); err != nil {
				t.Fatal(err)
			}
			if len(bodies) != 2 {
				t.Fatalf("requests = %d", len(bodies))
			}
			stripCache := func(items []any) []any {
				for _, item := range items {
					if blocks, ok := item.(map[string]any)["content"].([]any); ok {
						for _, block := range blocks {
							delete(block.(map[string]any), "cache_control")
						}
					}
				}
				return items
			}
			first := stripCache(bodies[0][field].([]any))
			second := stripCache(bodies[1][field].([]any))
			if len(second) < len(first) || !reflect.DeepEqual(first, second[:len(first)]) || !reflect.DeepEqual(bodies[0]["system"], bodies[1]["system"]) || !reflect.DeepEqual(bodies[0]["tools"], bodies[1]["tools"]) {
				t.Fatal("provider serialization rewrote prefix, system or tools")
			}
		})
	}
}
