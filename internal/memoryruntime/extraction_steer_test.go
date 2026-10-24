package memoryruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
)

// steerOnceClient is the chat model of a real agent.Agent: on its first
// request it steers the running turn, the way a user types while the agent
// works.
type steerOnceClient struct {
	fixtureClient
	ag    *agent.Agent
	steer string
	once  sync.Once
}

func (c *steerOnceClient) Clone() ai.Client { return c }

func (c *steerOnceClient) Stream(_ context.Context, _ ai.Request, _ func(string), _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.once.Do(func() { c.ag.Steer(c.steer) })
	return ai.Message{Role: "assistant", Content: "ok"}, ai.Usage{}, nil
}

// A message the user sends while a run is going (Steer) becomes a user
// message in History after turn.User, but the agent reports only turn.User to
// Completed. The extractor now puts every other user message in
// <earlier-user-messages context-only="true"> and tells the model they were
// "already handled on earlier turns", so a "remember" sent as a steer is never
// judged. The 1f27ade extractor judged the last four user messages.
func TestSteeredUserMessageIsJudged(t *testing.T) {
	extraction := &fixtureClient{}
	rt, _ := testRuntime(t, extraction)
	t.Cleanup(func() { _ = rt.Close() })
	steer := "记住：以后提交信息一律用英文写"
	chat := &steerOnceClient{steer: steer}
	ag := agent.New(chat, "m", 1000, "sys")
	chat.ag = ag
	ag.SetMemoryRuntime(rt)
	if _, err := ag.Turn(context.Background(), "帮我把这个函数改短一点", agent.Events{}); err != nil {
		t.Fatal(err)
	}
	steered := false
	for _, m := range ag.MessagesSnapshot() {
		if m.Role == "user" && m.TextContent() == steer {
			steered = true
		}
	}
	if !steered {
		t.Fatal("fixture: steer did not reach the transcript")
	}
	if !waitFor(t, func() bool { extraction.mu.Lock(); defer extraction.mu.Unlock(); return extraction.calls > 0 }) {
		t.Fatal("extraction never ran")
	}
	time.Sleep(100 * time.Millisecond)
	extraction.mu.Lock()
	defer extraction.mu.Unlock()
	for _, request := range extraction.requests {
		if judged(request.Messages[1].TextContent(), steer) {
			return
		}
	}
	t.Fatalf("steered message was only given as already-handled context in %d requests; prompt:\n%s", len(extraction.requests), extraction.requests[0].Messages[1].TextContent())
}

// archive moves a live entry into the store's .archive directory, which the
// store loads as Archived (store.go load) and Overview hides.
func archive(t *testing.T, store *memory.Store, rel string) {
	t.Helper()
	from := filepath.Join(store.Root(), filepath.FromSlash(rel))
	dir := filepath.Join(filepath.Dir(from), ".archive")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(from, filepath.Join(dir, filepath.Base(from))); err != nil {
		t.Fatal(err)
	}
}

// existingMemory switched from Overview (which skips archived entries) to
// List (which returns them). An archived slug now counts as known, so a
// low-confidence write under that slug passes the new-slug gate and a live
// entry is created from weak evidence.
func TestArchivedEntryDoesNotOpenNewSlugGate(t *testing.T) {
	client := &fixtureClient{payload: `{"decisions":[{"action":"write","slug":"reply-language","scope":"global","type":"user","description":"maybe English","body":"Reply in English?","confidence":"low"}]}`}
	rt, store := testRuntime(t, client)
	t.Cleanup(func() { _ = rt.Close() })
	if _, err := store.Write(memory.WriteArgs{Slug: "reply-language", Scope: "global", MemoryType: "user", Description: "Reply in Chinese", Body: "old"}); err != nil {
		t.Fatal(err)
	}
	archive(t, store, "global/user/reply-language.md")
	list, err := store.List(memory.ListArgs{})
	if err != nil || len(list.Entries) != 1 || !list.Entries[0].Archived {
		t.Fatalf("fixture: want one archived entry, got %+v err=%v", list.Entries, err)
	}
	rt.Completed(context.Background(), extractionTurn("this one in English I guess"))
	waitFor(t, func() bool { client.mu.Lock(); defer client.mu.Unlock(); return client.calls > 0 })
	time.Sleep(200 * time.Millisecond)
	list, err = store.List(memory.ListArgs{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range list.Entries {
		if e.Slug == "reply-language" && !e.Archived {
			client.mu.Lock()
			prompt := client.requests[0].Messages[1].TextContent()
			client.mu.Unlock()
			t.Fatalf("low-confidence write revived archived slug as a live entry %+v; archived entry listed in prompt=%v", e, strings.Contains(prompt, "reply-language"))
		}
	}
}

// busyThenMalformedClient: busy once, then a malformed plan, then (if nudged)
// a valid plan.
type busyThenMalformedClient struct {
	fixtureClient
}

func (c *busyThenMalformedClient) Clone() ai.Client { return c }

func (c *busyThenMalformedClient) Stream(_ context.Context, request ai.Request, _ func(string), _ func(string), onCall func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	c.calls++
	i := c.calls
	c.requests = append(c.requests, request)
	c.mu.Unlock()
	switch i {
	case 1:
		return ai.Message{}, ai.Usage{}, errors.New("API stream error: server_busy: Our servers are currently busy.")
	case 2:
		onCall("1", "SubmitMemoryPlan", `{"decisions":[{"action":"bogus","slug":"x"}]}`)
	default:
		onCall("1", "SubmitMemoryPlan", `{"decisions":[{"action":"write","slug":"user-pm","scope":"global","type":"user","description":"Uses pnpm","body":"Use pnpm.","confidence":"high","source_quote":"I prefer pnpm"}]}`)
	}
	return ai.Message{Role: "assistant"}, ai.Usage{}, nil
}

// The commit says a malformed plan is nudged once. When the malformed plan
// arrives on a busy retry, the retry loop breaks on planError and returns it
// without the nudge; extract then logs and drops the turn.
func TestMalformedPlanAfterBusyRetryIsNudged(t *testing.T) {
	fastExtractionRetries(t)
	client := &busyThenMalformedClient{}
	rt, store := testRuntime(t, client)
	t.Cleanup(func() { _ = rt.Close() })
	rt.Completed(context.Background(), extractionTurn("I prefer pnpm"))
	ok := waitFor(t, func() bool {
		_, err := store.Read(memory.ReadArgs{Slug: "user-pm", Scope: "global"})
		return err == nil
	})
	if !ok {
		client.mu.Lock()
		calls := client.calls
		client.mu.Unlock()
		t.Fatalf("malformed plan after a busy retry was never nudged; model calls = %d, want 3 (busy, malformed, nudge)", calls)
	}
}
