package memoryruntime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// stopFirstClient is the chat model of a real agent.Agent whose first request
// blocks until the run is cancelled, the way the backend's queue "interrupt"
// policy or the user's stop button cancels a running turn (queue.go rt.cancel).
type stopFirstClient struct {
	fixtureClient
	started chan struct{}
	once    sync.Once
	n       int
}

func (c *stopFirstClient) Clone() ai.Client { return c }

func (c *stopFirstClient) Stream(ctx context.Context, _ ai.Request, _ func(string), _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	c.n++
	n := c.n
	c.mu.Unlock()
	if n == 1 {
		c.once.Do(func() { close(c.started) })
		<-ctx.Done()
		return ai.Message{}, ai.Usage{}, ctx.Err()
	}
	return ai.Message{Role: "assistant", Content: "ok"}, ai.Usage{}, nil
}

// A cancelled (or failed) run never reaches MemoryTurn Completed
// (agent.go only calls it on a natural end), but its user message stays in
// History (server.go executeRun keeps it). On the next completed turn that
// message is only shown in <earlier-user-messages>, which the prompt calls
// "already handled on earlier turns, so never extract from them again". A
// "remember" in the stopped turn is never judged. 1f27ade judged the last four
// user messages, so it was still extracted.
func TestRememberInCancelledTurnIsJudgedOnNextTurn(t *testing.T) {
	extraction := &fixtureClient{}
	rt, _ := testRuntime(t, extraction)
	t.Cleanup(func() { _ = rt.Close() })
	chat := &stopFirstClient{started: make(chan struct{})}
	ag := agent.New(chat, "m", 1000, "sys")
	ag.SetMemoryRuntime(rt)

	remember := "记住：以后提交信息一律用英文写。先帮我跑一下全部测试"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := ag.Turn(ctx, remember, agent.Events{}); done <- err }()
	<-chat.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("fixture: first turn err = %v, want context.Canceled", err)
	}
	kept := false
	for _, m := range ag.MessagesSnapshot() {
		if m.Role == "user" && m.TextContent() == remember {
			kept = true
		}
	}
	if !kept {
		t.Fatal("fixture: cancelled turn's user message is not in History")
	}

	if _, err := ag.Turn(context.Background(), "测试太慢了，只跑 internal/memory 吧", agent.Events{}); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, func() bool { extraction.mu.Lock(); defer extraction.mu.Unlock(); return extraction.calls > 0 }) {
		t.Fatal("extraction never ran")
	}
	time.Sleep(100 * time.Millisecond)
	extraction.mu.Lock()
	defer extraction.mu.Unlock()
	for _, request := range extraction.requests {
		if judged(request.Messages[1].TextContent(), remember) {
			return
		}
	}
	prompt := extraction.requests[0].Messages[1].TextContent()
	t.Fatalf("remember from the cancelled turn was given only as already-handled context in %d requests; prompt:\n%s", len(extraction.requests), prompt[strings.Index(prompt, "<earlier-user-messages"):])
}
