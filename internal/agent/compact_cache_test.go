package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestCompactionReplaysPrefixAndToolsOnSameRoute(t *testing.T) {
	var request ai.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request = ai.Request{}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"verified summary"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	for _, mode := range []string{"same-route", "dedicated", "over-budget"} {
		a := New(ai.New(server.URL, "fixture"), "fixture", 100, "stable", WithTurnTimeContext())
		a.Tools = []tools.Tool{echoTool()}
		if mode == "dedicated" {
			a.CompactClient = ai.New(server.URL, "fixture")
		}
		if mode == "over-budget" {
			a.ContextLimit = 1000
		}
		for range 3 {
			a.Messages = append(a.Messages, ai.Message{Role: "user", Content: strings.Repeat("history ", 9000)}, ai.Message{Role: "assistant", Content: "answer"})
		}
		a.Messages[1].PromptSnapshots = []ai.PromptSnapshot{{Source: savedMemorySource, Text: "\n\nsaved context"}}
		before := a.MessagesSnapshot()
		_, cutoff, _, err := a.compact(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if mode != "same-route" {
			if len(request.Messages) != 2 || len(request.Tools) != 0 {
				t.Fatalf("%s must retain bounded text projection", mode)
			}
			continue
		}
		wantTools, _ := json.Marshal(tools.Defs(a.AllTools()))
		gotTools, _ := json.Marshal(request.Tools)
		var want, got any
		if err := json.Unmarshal(wantTools, &want); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(gotTools, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("summary request changed tool definitions: got %s want %s", gotTools, wantTools)
		}
		if len(request.Messages) != cutoff+1 || request.Messages[cutoff].Content != cachedSummaryInstruction {
			t.Fatal("summary directive must follow replayed prefix")
		}
		prefix := withTurnTimes(withPromptContext(before[:cutoff]))
		if !reflect.DeepEqual(prefix, request.Messages[:cutoff]) {
			t.Fatal("summary rewrote model-visible history")
		}
	}
}

func TestEmptySummaryPreservesHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	a := New(ai.New(server.URL, "fixture"), "fixture", 100, "stable")
	a.Messages = append(a.Messages, ai.Message{Role: "user", Content: "first"}, ai.Message{Role: "assistant", Content: "answer"}, ai.Message{Role: "user", Content: "next"})
	before := a.MessagesSnapshot()
	if _, _, _, err := a.compact(t.Context()); err == nil {
		t.Fatal("empty summary accepted")
	}
	if !reflect.DeepEqual(before, a.MessagesSnapshot()) {
		t.Fatal("failed summary discarded history")
	}
}
