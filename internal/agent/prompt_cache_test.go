package agent

import (
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
)

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
