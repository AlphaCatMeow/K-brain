package memoryruntime

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
)

// cancelFirstTurn runs text as a turn whose first model request blocks until
// the run is cancelled (stop button / queue interrupt), and returns the tail
// user message the backend later resumes (run_start.go ResumeMessageID).
func cancelFirstTurn(t *testing.T, ag *agent.Agent, chat *stopFirstClient, text string) ai.Message {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := ag.Turn(ctx, text, agent.Events{}); done <- err }()
	<-chat.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("fixture: cancelled turn err = %v", err)
	}
	msgs := ag.MessagesSnapshot()
	tail := msgs[len(msgs)-1]
	if tail.Role != "user" || tail.TextContent() != text || tail.ID == "" {
		t.Fatalf("fixture: tail after cancel = %+v, want the user message", tail)
	}
	return tail
}

func judgedRequests(c *fixtureClient, text string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, request := range c.requests {
		if judged(request.Messages[1].TextContent(), text) {
			n++
		}
	}
	return n
}

// Stop before the first reply leaves the user message as the tail; the client
// then resumes it (ContinueUser, server.go executeRun). bd95f38 reports it from
// the defer on cancel, and the resumed run reports the same message again on
// its natural end. Completed only dedupes against a still-pending turn
// (runtime.go c.pending.User.ID), so the message is judged twice and an
// append_daily bullet is written twice.
func TestStoppedThenResumedTurnIsJudgedOnce(t *testing.T) {
	extraction := &fixtureClient{payload: `{"decisions":[{"action":"append_daily","body":"迁移了构建到 pnpm workspace"}]}`}
	rt, store := testRuntime(t, extraction)
	t.Cleanup(func() { _ = rt.Close() })
	chat := &stopFirstClient{started: make(chan struct{})}
	ag := agent.New(chat, "m", 1000, "sys")
	ag.SetMemoryRuntime(rt)

	text := "今天把构建迁移到了 pnpm workspace，记一下"
	tail := cancelFirstTurn(t, ag, chat, text)
	waitFor(t, func() bool { return judgedRequests(extraction, text) > 0 })
	time.Sleep(50 * time.Millisecond)

	if _, err := ag.ContinueUser(context.Background(), tail.ID, agent.Events{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	daily, _ := store.Read(memory.ReadArgs{Slug: "daily-" + time.Now().Format("2006-01-02"), Scope: "global"})
	bullets := strings.Count(daily.Body, "迁移了构建到 pnpm workspace")
	if n := judgedRequests(extraction, text); n != 1 || bullets != 1 {
		t.Fatalf("stopped-then-resumed message judged in %d extraction requests (want 1); daily bullets = %d (want 1)\n%s", n, bullets, daily.Body)
	}
}

// Stop, then edit-and-resend (history.go mutateHistory edit: the tail user
// message is replaced and the session reloaded, keeping the memory runtime;
// session/mutation.go gives the replacement a new ID). When the stop lands
// within the throttle window, bd95f38 parks the stopped message as pending;
// the edited message is appended to unjudged, and the extractor later judges
// the withdrawn text although it is no longer anywhere in History.
func TestStoppedThenEditedMessageIsNotJudged(t *testing.T) {
	extraction := &fixtureClient{}
	store, err := memory.OpenStore(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := New(Config{Store: store, ResolveModel: func(context.Context, string) (ai.Client, string, error) { return extraction, "fixture", nil }, ExtractionModel: "fixture", Throttle: 400 * time.Millisecond, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	chat := &stopFirstClient{started: make(chan struct{})}
	ag := agent.New(chat, "m", 1000, "sys")
	ag.SetMemoryRuntime(rt)
	// Turn 1 completes normally (the client only blocks its first request, so
	// open with the stopped turn and use the earlier extraction from a direct
	// Completed to start the throttle window).
	rt.Completed(context.Background(), agent.MemoryTurn{User: ai.Message{ID: "u0", Role: "user", Content: "帮我看一下这个仓库的目录结构"}})
	waitFor(t, func() bool { extraction.mu.Lock(); defer extraction.mu.Unlock(); return extraction.calls > 0 })

	withdrawn := "记住：以后所有回复都用英文"
	cancelFirstTurn(t, ag, chat, withdrawn)

	// Edit-and-resend: the backend replaces the tail and reloads history.
	var kept []ai.Message
	for _, m := range ag.MessagesSnapshot() {
		if m.Role == "user" && m.TextContent() == withdrawn {
			continue
		}
		kept = append(kept, m)
	}
	edited := ai.Message{ID: "msg-edited", Role: "user", Content: "以后所有回复都用中文", Authored: true}
	ag.RestoreMessages(append(kept, edited))
	if _, err := ag.ContinueUser(context.Background(), edited.ID, agent.Events{}); err != nil {
		t.Fatal(err)
	}
	for _, m := range ag.MessagesSnapshot() {
		if strings.Contains(m.TextContent(), withdrawn) {
			t.Fatal("fixture: withdrawn message still in History")
		}
	}
	waitFor(t, func() bool { return judgedRequests(extraction, edited.Content) > 0 })
	time.Sleep(100 * time.Millisecond)
	if n := judgedRequests(extraction, withdrawn); n != 0 {
		extraction.mu.Lock()
		prompt := extraction.requests[len(extraction.requests)-1].Messages[1].TextContent()
		extraction.mu.Unlock()
		t.Fatalf("message the user stopped and edited away was judged in %d requests; last prompt:\n%s", n, prompt[strings.Index(prompt, "<latest-user-message>"):])
	}
}
