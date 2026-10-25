package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// review9Server fails the first request with a non-retryable 400 (the turn errors and
// its user message stays in History as the tail, as the backend persists it for a
// retry) and answers later requests, recording what the model was sent.
func review9Server(t *testing.T) (*httptest.Server, *[]ai.Request) {
	t.Helper()
	var requests []ai.Request
	n := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ai.Request
		_ = json.NewDecoder(r.Body).Decode(&request)
		n++
		if n == 1 {
			http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
			return
		}
		requests = append(requests, request)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func review9RequestText(request ai.Request) string {
	var text strings.Builder
	for _, message := range request.Messages {
		if message.Role == "user" {
			text.WriteString(message.Content)
			for _, part := range message.Parts {
				text.WriteString(part.Text)
			}
		}
	}
	return text.String()
}

// A turn sent while memory is off fails (provider error), so its user message, which
// carries the off instruction and the memory-off marker, stays as the History tail.
// The user turns memory back on and retries it (ContinueUser, the backend's
// resume_message_id path). contextSnapshots computes the on-again instruction on the
// memory-switch source, but markResumedMessage only copies saved/runtime/memory-off
// snapshots into the resumed message, so the request still ends with "do not save
// memories ... tell the user memory is turned off" while memory tools are back.
func TestReview9ResumedOffMessageLiftsOffInstructionWhenMemoryIsOnAgain(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	server, requests := review9Server(t)
	a := New(ai.New(server.URL, "fixture"), "fixture", 100, "system")
	a.Tools = nil
	a.SetMemoryEnabled(false)
	if _, err := a.TurnAuthored(t.Context(), "remember that I prefer tabs", Events{}); err == nil {
		t.Fatal("fixture: the first request must fail")
	}
	tail := a.MessagesSnapshot()[len(a.MessagesSnapshot())-1]
	if tail.Role != "user" || !isMemoryOffMessage(tail) || !strings.Contains(promptContextText(tail), "Memory is turned off") {
		t.Fatalf("fixture: failed off turn must leave its user message as the tail with the off instruction: %#v", tail)
	}

	a.SetMemoryEnabled(true)
	if _, err := a.ContinueUser(t.Context(), tail.ID, Events{}); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 1 {
		t.Fatalf("requests = %d", len(*requests))
	}
	sent := review9RequestText((*requests)[0])
	off := strings.LastIndex(sent, "Memory is turned off")
	on := strings.LastIndex(sent, "Memory is turned on again")
	if on < off {
		t.Fatalf("resumed turn with memory on still ends with the off instruction (off at %d, on-again at %d); last instruction in History = %q",
			off, on, lastMemorySwitchInstruction(a.MessagesSnapshot()))
	}
}

// Same retry, followed by a normal turn: the on-again instruction must still be sent
// exactly once across the session, not dropped on the retry and sent again later.
func TestReview9OnAgainInstructionPersistedOnceAcrossResumeAndNextTurn(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	server, requests := review9Server(t)
	a := New(ai.New(server.URL, "fixture"), "fixture", 100, "system")
	a.Tools = nil
	a.SetMemoryEnabled(false)
	_, _ = a.TurnAuthored(t.Context(), "first", Events{})
	tail := a.MessagesSnapshot()[len(a.MessagesSnapshot())-1]
	a.SetMemoryEnabled(true)
	if _, err := a.ContinueUser(t.Context(), tail.ID, Events{}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.TurnAuthored(t.Context(), "second", Events{}); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 2 {
		t.Fatalf("requests = %d", len(*requests))
	}
	first := review9RequestText((*requests)[0])
	second := review9RequestText((*requests)[1])
	if strings.Count(first, "Memory is turned on again") != 1 {
		t.Fatalf("the retry request (first with memory on) must carry the on-again instruction once; got %d", strings.Count(first, "Memory is turned on again"))
	}
	if strings.Count(second, "Memory is turned on again") != 1 {
		t.Fatalf("the next request must still carry exactly one on-again instruction; got %d", strings.Count(second, "Memory is turned on again"))
	}
}
