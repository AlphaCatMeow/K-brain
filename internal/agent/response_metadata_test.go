package agent

import (
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"testing"
	"time"
)

func TestAppendResponseStableMetadata(t *testing.T) {
	a := &Agent{Model: "model", Provider: "provider"}
	before := time.Now()
	a.appendResponse(ai.Message{Role: "assistant", Content: "reply"}, ai.Usage{CompletionTokens: 2})
	got := a.MessagesSnapshot()[0]
	if got.ID == "" || got.SentAt == nil || got.SentAt.Before(before) || got.SentAt.After(time.Now()) {
		t.Fatalf("missing response identity or time: %+v", got)
	}
	a.appendResponse(got, ai.Usage{})
	next := a.MessagesSnapshot()[1]
	if next.ID != got.ID || !next.SentAt.Equal(*got.SentAt) {
		t.Fatalf("response metadata changed: %+v", next)
	}
}
