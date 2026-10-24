package memoryruntime

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// gatedFailClient blocks the first extraction request until release is
// closed, then fails it with a permanent 4xx so no retry delay runs.
type gatedFailClient struct {
	fixtureClient
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *gatedFailClient) Clone() ai.Client { return c }

func (c *gatedFailClient) Stream(ctx context.Context, request ai.Request, _ func(string), _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	c.requests = append(c.requests, request)
	c.mu.Unlock()
	c.once.Do(func() { close(c.started) })
	select {
	case <-c.release:
	case <-ctx.Done():
		return ai.Message{}, ai.Usage{}, ctx.Err()
	}
	return ai.Message{}, ai.Usage{}, &ai.HTTPError{StatusCode: 400, Status: "400 Bad Request"}
}

// memory_wipe_all (store_dispatch.go -> Store.WipeAll) now sets
// state.Unjudged = nil, but nothing tells the per-session runtimes: an
// extraction already running keeps its judge slice and, when the request
// fails, holdUnlessClosed (the runtime is not closed) writes the message
// straight back into .kbrain-state.json after the wipe. The next completed
// turn in that session then judges it again.
func TestWipeAllDuringFailingExtractionDoesNotReholdMessage(t *testing.T) {
	client := &gatedFailClient{started: make(chan struct{}), release: make(chan struct{})}
	rt, store := testRuntime(t, client)
	t.Cleanup(func() { _ = rt.Close() })
	secret := "记住：我家 Wi-Fi 密码是 hunter2"
	u1 := ai.Message{ID: "u1", Role: "user", Content: secret}
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u1, History: []ai.Message{u1}})
	<-client.started
	if _, err := store.WipeAll(); err != nil {
		t.Fatal(err)
	}
	close(client.release)
	settle(t, rt, "s")
	held, err := store.TakeUnjudged("s")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range held {
		if strings.Contains(h.Text, "hunter2") {
			t.Fatalf("after memory_wipe_all the in-flight extraction re-held %q into the state file", h.Text)
		}
	}
}

// Same wipe, but the extraction is still queued in the claim (throttled) when
// the runtime is later closed by the idle reaper / model switch: Close holds
// c.unjudged back into the wiped state.
func TestWipeAllThenCloseDoesNotReholdQueuedMessage(t *testing.T) {
	client := &gatedFailClient{started: make(chan struct{}), release: make(chan struct{})}
	rt, store := testRuntime(t, client)
	u1 := ai.Message{ID: "u1", Role: "user", Content: "记住：以后都用中文回答"}
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u1, History: []ai.Message{u1}})
	<-client.started
	if _, err := store.WipeAll(); err != nil {
		t.Fatal(err)
	}
	_ = rt.Close() // idle reap: cancels the extraction and holds its inflight messages
	settle(t, rt, "s")
	held, err := store.TakeUnjudged("s")
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 0 {
		t.Fatalf("Close after memory_wipe_all held %d message(s) back into the wiped state: %q", len(held), held[0].Text)
	}
}

// Held text is TrimSpace'd before truncation (hold), while extractionMessages
// keys History text without trimming, so a held message with surrounding
// whitespace is not recognised as judged and appears in both
// <earlier-user-messages> and <latest-user-message>.
func TestHeldMessageWithWhitespaceIsNotRepeatedAsEarlierContext(t *testing.T) {
	client := &fixtureClient{}
	rt, _ := testRuntime(t, client)
	t.Cleanup(func() { _ = rt.Close() })
	remember := "记住：以后都用中文回答"
	u1 := ai.Message{ID: "u1", Role: "user", Content: remember + "\n"}
	rt.Abandoned("s", u1)
	u2 := ai.Message{ID: "u2", Role: "user", Content: "帮我看看这个函数为什么慢"}
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u2, History: []ai.Message{u1, {Role: "assistant", Content: "stopped"}, u2}})
	settle(t, rt, "s")
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.requests) == 0 {
		t.Fatal("no extraction request")
	}
	prompt := client.requests[0].Messages[1].TextContent()
	if n := strings.Count(prompt, remember); n != 1 {
		t.Fatalf("held message appears %d times in the extraction prompt:\n%s", n, prompt)
	}
}
