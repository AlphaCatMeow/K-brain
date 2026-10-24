package memoryruntime

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
)

// gatedThenSubmitClient blocks the first extraction until release, fails it
// with a permanent 400, then answers every later extraction with the
// fixture's upsert plan.
type gatedThenSubmitClient struct {
	fixtureClient
	started chan struct{}
	release chan struct{}
	first   sync.Once
}

func (c *gatedThenSubmitClient) Clone() ai.Client { return c }

func (c *gatedThenSubmitClient) Stream(ctx context.Context, request ai.Request, a func(string), b func(string), onCall func(string, string, string)) (ai.Message, ai.Usage, error) {
	gated := false
	c.first.Do(func() { gated = true })
	if !gated {
		return c.fixtureClient.Stream(ctx, request, a, b, onCall)
	}
	c.mu.Lock()
	c.requests = append(c.requests, request)
	c.mu.Unlock()
	close(c.started)
	select {
	case <-c.release:
	case <-ctx.Done():
		return ai.Message{}, ai.Usage{}, ctx.Err()
	}
	return ai.Message{}, ai.Usage{}, &ai.HTTPError{StatusCode: 400, Status: "400 Bad Request"}
}

// unjudgedEpoch is read only when c.unjudged is empty. A turn queued before
// a wipe (u1, waiting behind a running extraction or the 30s throttle) pins
// the queue to the old epoch; a turn completed after the wipe (u2) is
// appended to the same queue, so the extraction that judges u2 sees the
// epoch changed and drops its plan. u2 was said after the wipe and must be
// remembered.
func TestMessageCompletedAfterWipeIsRememberedWhenQueueStartedBeforeWipe(t *testing.T) {
	client := &gatedThenSubmitClient{started: make(chan struct{}), release: make(chan struct{})}
	client.submit = true
	rt, store := testRuntime(t, client)
	t.Cleanup(func() { _ = rt.Close() })
	u0 := ai.Message{ID: "u0", Role: "user", Content: "记住：我喜欢简洁的回答"}
	u1 := ai.Message{ID: "u1", Role: "user", Content: "记住：项目用 Go 1.24"}
	u2 := ai.Message{ID: "u2", Role: "user", Content: "记住：以后回答简洁一点"}
	a := ai.Message{Role: "assistant", Content: "ok"}
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u0, History: []ai.Message{u0}})
	<-client.started
	// u1 completes while u0's extraction runs: queued at the pre-wipe epoch.
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u1, History: []ai.Message{u0, a, u1}})
	if _, err := store.WipeAll(); err != nil {
		t.Fatal(err)
	}
	// u2 is said after the wipe.
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u2, History: []ai.Message{u0, a, u1, a, u2}})
	close(client.release)
	waitFor(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return len(client.requests) >= 2
	})
	settle(t, rt, "s")
	client.mu.Lock()
	n := len(client.requests)
	judged := n >= 2 && strings.Contains(client.requests[n-1].Messages[1].TextContent(), u2.Content)
	client.mu.Unlock()
	if !judged {
		t.Fatalf("post-wipe message u2 was never sent to the extractor (requests=%d)", n)
	}
	list, err := store.List(memory.ListArgs{IncludeAllProjects: true, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Entries) == 0 {
		t.Fatalf("extractor judged post-wipe message u2 and returned a plan, but it was dropped: the queue kept the epoch read when pre-wipe u1 was queued")
	}
}

// gatedSubmitClient blocks the first extraction until release, then answers
// every extraction with the fixture's upsert plan.
type gatedSubmitClient struct {
	fixtureClient
	started chan struct{}
	release chan struct{}
	first   sync.Once
}

func (c *gatedSubmitClient) Clone() ai.Client { return c }

func (c *gatedSubmitClient) Stream(ctx context.Context, request ai.Request, a func(string), b func(string), onCall func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.first.Do(func() {
		close(c.started)
		select {
		case <-c.release:
		case <-ctx.Done():
		}
	})
	return c.fixtureClient.Stream(ctx, request, a, b, onCall)
}

// The runtime treats OrganizerEpoch as the memory wipe epoch, but
// memory_organize_run_clear_history (Store.ClearOrganizeHistory, the memory
// panel's "clear organizer history") also bumps it without touching any
// memory. An extraction running at that moment drops its plan.
func TestClearingOrganizerHistoryDoesNotDropRunningExtraction(t *testing.T) {
	client := &gatedSubmitClient{started: make(chan struct{}), release: make(chan struct{})}
	client.submit = true
	rt, store := testRuntime(t, client)
	t.Cleanup(func() { _ = rt.Close() })
	u1 := ai.Message{ID: "u1", Role: "user", Content: "记住：我喜欢简洁的回答"}
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u1, History: []ai.Message{u1}})
	<-client.started
	if _, err := store.Dispatch("memory_organize_run_clear_history", nil); err != nil {
		t.Fatal(err)
	}
	close(client.release)
	settle(t, rt, "s")
	list, err := store.List(memory.ListArgs{IncludeAllProjects: true, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Entries) == 0 {
		t.Fatal("clearing organizer history (no memory wiped) made the running extraction drop its plan")
	}
}
