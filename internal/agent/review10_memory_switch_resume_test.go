package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
)

// review10Server fails the first `fail` requests with a non-retryable 400 (the turn
// errors and its user message stays in History as the tail) and answers the rest,
// recording what the model was sent on every request, failed or not.
func review10Server(t *testing.T, fail int) (*httptest.Server, *[]ai.Request) {
	t.Helper()
	var requests []ai.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ai.Request
		_ = json.NewDecoder(r.Body).Decode(&request)
		requests = append(requests, request)
		if len(requests) <= fail {
			http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func review10Agent(t *testing.T, fail int) (*Agent, *[]ai.Request) {
	t.Helper()
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	server, requests := review10Server(t, fail)
	a := New(ai.New(server.URL, "fixture"), "fixture", 100, "system")
	a.Tools = nil
	return a, requests
}

func review10Tail(a *Agent) ai.Message {
	msgs := a.MessagesSnapshot()
	return msgs[len(msgs)-1]
}

func review10Count(m ai.Message, pred func(ai.PromptSnapshot) bool) int {
	n := 0
	for _, s := range m.PromptSnapshots {
		if pred(s) {
			n++
		}
	}
	return n
}

func review10Source(source string) func(ai.PromptSnapshot) bool {
	return func(s ai.PromptSnapshot) bool { return s.Source == source }
}

func review10Text(text string) func(ai.PromptSnapshot) bool {
	return func(s ai.PromptSnapshot) bool { return s.Text == text }
}

// Scenario 1: memory on, the tail was written while on and History never saw an off
// instruction. Saved memory changes between the failed turn and the retry. The resume
// must leave the persisted tail exactly as it was (the pre-da30c45 early return), and
// contextSnapshots must not compute a memory-switch snapshot.
func TestReview10OnResumeOfNeverOffMessageWritesNothingBack(t *testing.T) {
	a, requests := review10Agent(t, 1)
	if err := memory.Installation().Remember("user prefers tabs"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.TurnAuthored(t.Context(), "hello", Events{}); err == nil {
		t.Fatal("fixture: the first request must fail")
	}
	before := review10Tail(a)
	if before.Role != "user" || isMemoryOffMessage(before) || !strings.Contains(promptContextText(before), "user prefers tabs") {
		t.Fatalf("fixture: tail must be an on-written user message carrying saved memory: %#v", before)
	}
	// Change saved memory so the retry computes a fresh saved-memory snapshot.
	if err := memory.Installation().Remember("user prefers dark mode"); err != nil {
		t.Fatal(err)
	}
	a.RefreshMemory()
	computed := a.contextSnapshots()
	if slices.ContainsFunc(computed, review10Source(memorySwitchSource)) {
		t.Fatalf("contextSnapshots computed a memory-switch snapshot without any off instruction in History: %#v", computed)
	}
	if !slices.ContainsFunc(computed, review10Source(savedMemorySource)) {
		t.Fatalf("fixture: changed saved memory must yield a saved-memory snapshot: %#v", computed)
	}

	if _, err := a.ContinueUser(t.Context(), before.ID, Events{}); err != nil {
		t.Fatal(err)
	}
	after := review10Tail(a)
	if after.Role == "user" {
		t.Fatalf("fixture: the retry must succeed and append an answer")
	}
	resumed := a.MessagesSnapshot()[len(a.MessagesSnapshot())-2]
	if resumed.ID != before.ID || !reflect.DeepEqual(resumed.PromptSnapshots, before.PromptSnapshots) {
		t.Fatalf("resume of a never-off message changed its snapshots:\nbefore %#v\nafter  %#v", before.PromptSnapshots, resumed.PromptSnapshots)
	}
	if sent := review9RequestText((*requests)[1]); strings.Contains(sent, "Memory is turned") {
		t.Fatalf("retry of a never-off message mentions the switch: %q", sent)
	}
}

// Scenario 2: History carries an earlier, unlifted off instruction on another message
// (an off turn that completed), and the tail is a message written while on that does
// not carry the on-again instruction (a session persisted by an older build or a
// steered message). Resuming with memory on must write the on-again instruction once;
// a second retry must not repeat it, and the following turn must not either.
func TestReview10OnResumeAfterEarlierOffInstructionLiftsOnce(t *testing.T) {
	a, requests := review10Agent(t, 1)
	a.Messages = []ai.Message{
		{ID: "x", Role: "user", Content: "off turn", PromptSnapshots: []ai.PromptSnapshot{{Source: runtimeMemorySource, Text: memoryOffNotice}, MemoryOffSnapshot()}},
		{Role: "assistant", Content: "ok"},
		{ID: "y", Role: "user", Content: "written while on"},
	}
	if got := lastMemorySwitchInstruction(a.Messages); got != memoryOffNotice {
		t.Fatalf("fixture: History must end on the off instruction, got %q", got)
	}
	if _, err := a.ContinueUser(t.Context(), "y", Events{}); err == nil {
		t.Fatal("fixture: first retry must fail")
	}
	tail := review10Tail(a)
	if tail.ID != "y" || isMemoryOffMessage(tail) {
		t.Fatalf("fixture: tail must stay the on-written message: %#v", tail)
	}
	if n := review10Count(tail, review10Text(memoryOnNotice)); n != 1 {
		t.Fatalf("first retry wrote %d on-again instructions to the tail, want 1: %#v", n, tail.PromptSnapshots)
	}
	if _, err := a.ContinueUser(t.Context(), "y", Events{}); err != nil {
		t.Fatal(err)
	}
	resumed := a.MessagesSnapshot()[2]
	if n := review10Count(resumed, review10Text(memoryOnNotice)); n != 1 {
		t.Fatalf("second retry left %d on-again instructions on the tail, want 1: %#v", n, resumed.PromptSnapshots)
	}
	if isMemoryOffMessage(resumed) {
		t.Fatalf("on-written message gained the memory-off marker: %#v", resumed.PromptSnapshots)
	}
	if _, err := a.TurnAuthored(t.Context(), "next", Events{}); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 3 {
		t.Fatalf("requests = %d", len(*requests))
	}
	for i, request := range *requests {
		if n := strings.Count(review9RequestText(request), "Memory is turned on again"); n != 1 {
			t.Fatalf("request %d carries %d on-again instructions, want 1", i, n)
		}
	}
}

// Scenario 3: an off-written message is retried three times after memory is turned
// back on (two failures, then success). On-again, the off marker and the re-injected
// saved memory must each appear once on the persisted message.
func TestReview10RepeatedOnResumeOfOffMessageDoesNotDuplicate(t *testing.T) {
	a, requests := review10Agent(t, 3)
	if err := memory.Installation().Remember("user prefers tabs"); err != nil {
		t.Fatal(err)
	}
	a.SetMemoryEnabled(false)
	if _, err := a.TurnAuthored(t.Context(), "remember that I like go", Events{}); err == nil {
		t.Fatal("fixture: the off turn must fail")
	}
	id := review10Tail(a).ID
	a.SetMemoryEnabled(true)
	for i := 0; i < 2; i++ {
		if _, err := a.ContinueUser(t.Context(), id, Events{}); err == nil {
			t.Fatalf("fixture: retry %d must fail", i+1)
		}
	}
	if _, err := a.ContinueUser(t.Context(), id, Events{}); err != nil {
		t.Fatal(err)
	}
	msgs := a.MessagesSnapshot()
	resumed := msgs[len(msgs)-2]
	if resumed.ID != id {
		t.Fatalf("fixture: resumed message moved: %#v", msgs)
	}
	if n := review10Count(resumed, review10Text(memoryOnNotice)); n != 1 {
		t.Fatalf("on-again instruction appears %d times on the resumed message, want 1: %#v", n, resumed.PromptSnapshots)
	}
	if n := review10Count(resumed, review10Source(memoryOffSource)); n != 1 {
		t.Fatalf("memory-off marker appears %d times, want 1: %#v", n, resumed.PromptSnapshots)
	}
	saved := review10Count(resumed, func(s ai.PromptSnapshot) bool {
		return s.Source == savedMemorySource && strings.Contains(s.Text, "user prefers tabs")
	})
	if saved != 1 {
		t.Fatalf("re-injected saved memory appears %d times, want 1: %#v", saved, resumed.PromptSnapshots)
	}
	if n := strings.Count(review9RequestText((*requests)[len(*requests)-1]), "Memory is turned on again"); n != 1 {
		t.Fatalf("final retry request carries %d on-again instructions, want 1", n)
	}
}

// Scenario 4: memory off. Resuming either an on-written or an off-written tail (twice)
// must never write a memory-switch snapshot, and the off instruction and marker must
// each appear once.
func TestReview10OffResumeNeverWritesMemorySwitch(t *testing.T) {
	for _, writtenOff := range []bool{false, true} {
		t.Run(fmt.Sprintf("writtenOff=%v", writtenOff), func(t *testing.T) {
			a, requests := review10Agent(t, 2)
			if err := memory.Installation().Remember("user prefers tabs"); err != nil {
				t.Fatal(err)
			}
			a.SetMemoryEnabled(!writtenOff)
			if _, err := a.TurnAuthored(t.Context(), "hello", Events{}); err == nil {
				t.Fatal("fixture: the first turn must fail")
			}
			id := review10Tail(a).ID
			a.SetMemoryEnabled(false)
			if computed := a.contextSnapshots(); slices.ContainsFunc(computed, review10Source(memorySwitchSource)) {
				t.Fatalf("contextSnapshots computed memory-switch while off: %#v", computed)
			}
			if _, err := a.ContinueUser(t.Context(), id, Events{}); err == nil {
				t.Fatal("fixture: first retry must fail")
			}
			if _, err := a.ContinueUser(t.Context(), id, Events{}); err != nil {
				t.Fatal(err)
			}
			msgs := a.MessagesSnapshot()
			resumed := msgs[len(msgs)-2]
			if n := review10Count(resumed, review10Source(memorySwitchSource)); n != 0 {
				t.Fatalf("off resume wrote %d memory-switch snapshots: %#v", n, resumed.PromptSnapshots)
			}
			if n := review10Count(resumed, review10Text(memoryOffNotice)); n != 1 {
				t.Fatalf("off instruction appears %d times, want 1: %#v", n, resumed.PromptSnapshots)
			}
			if n := review10Count(resumed, review10Source(memoryOffSource)); n != 1 {
				t.Fatalf("memory-off marker appears %d times, want 1: %#v", n, resumed.PromptSnapshots)
			}
			last := review9RequestText((*requests)[len(*requests)-1])
			if strings.Contains(last, "Memory is turned on again") {
				t.Fatalf("off retry request carries the on-again instruction: %q", last)
			}
			if strings.LastIndex(last, "Memory is turned off") < strings.LastIndex(last, "user prefers tabs") {
				t.Fatalf("off retry request ends with saved memory after the off instruction: %q", last)
			}
		})
	}
}
