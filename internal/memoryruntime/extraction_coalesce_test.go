package memoryruntime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
)

// gatedClient blocks its first request until release is closed and records
// every request it sees.
type gatedClient struct {
	fixtureClient
	gate     sync.Mutex
	started  chan struct{}
	release  chan struct{}
	seen     int
	requests []ai.Request
}

func (c *gatedClient) Clone() ai.Client { return c }

func (c *gatedClient) Stream(ctx context.Context, request ai.Request, _ func(string), _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.gate.Lock()
	c.seen++
	first := c.seen == 1
	c.requests = append(c.requests, request)
	c.gate.Unlock()
	if first {
		close(c.started)
		select {
		case <-c.release:
		case <-ctx.Done():
			return ai.Message{}, ai.Usage{}, ctx.Err()
		}
	}
	return ai.Message{Role: "assistant"}, ai.Usage{}, nil
}

// judged reports whether text reaches the extractor as something to judge,
// not only as context it is told was already handled.
func judged(prompt, text string) bool {
	if !strings.Contains(prompt, text) {
		return false
	}
	start := strings.Index(prompt, "<earlier-user-messages")
	end := strings.Index(prompt, "</earlier-user-messages>")
	if start < 0 || end < 0 {
		return true
	}
	return strings.Contains(prompt[:start]+prompt[end:], text)
}

// While one extraction is in flight, a second and a third turn complete. The
// claim keeps only the latest pending turn, and the prompt now tells the model
// that every earlier user message was already handled. The second turn is
// never judged, so a "remember ..." said there is lost.
func TestCoalescedPendingTurnIsStillJudged(t *testing.T) {
	client := &gatedClient{started: make(chan struct{}), release: make(chan struct{})}
	rt, _ := testRuntime(t, client)
	t.Cleanup(func() { _ = rt.Close() })
	t1 := ai.Message{ID: "u1", Role: "user", Content: "帮我看看这个报错"}
	second := "记住：以后都用 pnpm，别给我 npm 命令"
	t2 := ai.Message{ID: "u2", Role: "user", Content: second}
	t3 := ai.Message{ID: "u3", Role: "user", Content: "继续把测试跑一下"}
	a := ai.Message{Role: "assistant", Content: "ok"}
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: t1, History: []ai.Message{t1}})
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("first extraction never started")
	}
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: t2, History: []ai.Message{t1, a, t2}})
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: t3, History: []ai.Message{t1, a, t2, a, t3}})
	close(client.release)
	waitFor(t, func() bool { client.gate.Lock(); defer client.gate.Unlock(); return client.seen >= 2 })
	time.Sleep(200 * time.Millisecond)
	client.gate.Lock()
	defer client.gate.Unlock()
	for _, request := range client.requests {
		if judged(request.Messages[1].TextContent(), second) {
			return
		}
	}
	last := client.requests[len(client.requests)-1]
	t.Fatalf("turn 2 was never judged in %d requests; last prompt:\n%s", len(client.requests), last.Messages[1].TextContent())
}

// Overview keeps at most 30 entries per bucket. With 31 user entries the 31st
// is neither shown to the extractor nor known to the new-slug gate, so a
// medium-confidence rewrite of that existing entry is dropped as a weak new
// memory, while the same rewrite of a listed entry would be applied.
func TestExtractionGateKnowsEntriesBeyondOverviewCap(t *testing.T) {
	client := &fixtureClient{payload: `{"decisions":[{"action":"write","slug":"zz-reply-language","scope":"global","type":"user","description":"Reply in Chinese","body":"updated","confidence":"medium"}]}`}
	rt, store := testRuntime(t, client)
	t.Cleanup(func() { _ = rt.Close() })
	// Overview lists newest first, so the oldest entry is the one left out.
	if _, err := store.Write(memory.WriteArgs{Slug: "zz-reply-language", Scope: "global", MemoryType: "user", Description: "Reply in Chinese", Body: "old"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		time.Sleep(2 * time.Millisecond)
		if _, err := store.Write(memory.WriteArgs{Slug: fmt.Sprintf("aa-pref-%02d", i), Scope: "global", MemoryType: "user", Description: "pref", Body: "b"}); err != nil {
			t.Fatal(err)
		}
	}
	rt.Completed(context.Background(), extractionTurn("以后回答都用中文"))
	waitFor(t, func() bool { client.mu.Lock(); defer client.mu.Unlock(); return client.calls > 0 })
	ok := waitFor(t, func() bool {
		got, err := store.Read(memory.ReadArgs{Slug: "zz-reply-language", Scope: "global"})
		return err == nil && got.Body == "updated"
	})
	if !ok {
		client.mu.Lock()
		prompt := client.requests[0].Messages[1].TextContent()
		client.mu.Unlock()
		t.Fatalf("update of an existing entry beyond the overview cap was dropped; listed in prompt=%v", strings.Contains(prompt, "zz-reply-language"))
	}
}

// The gate's known set is keyed by slug only. A low-confidence write of a
// brand-new project entry passes because a global entry has the same slug.
func TestWeakNewProjectEntryIsGatedDespiteGlobalSlug(t *testing.T) {
	client := &fixtureClient{payload: `{"decisions":[{"action":"write","slug":"reply-style","scope":"project","type":"project","description":"guess","body":"maybe terse here","confidence":"low"}]}`}
	rt, store := testRuntime(t, client)
	t.Cleanup(func() { _ = rt.Close() })
	workdir := t.TempDir()
	if _, err := store.Write(memory.WriteArgs{Slug: "reply-style", Scope: "global", MemoryType: "user", Description: "Concise", Body: "Be concise."}); err != nil {
		t.Fatal(err)
	}
	turn := extractionTurn("这里先简短点吧")
	turn.Workdir = workdir
	rt.Completed(context.Background(), turn)
	waitFor(t, func() bool { client.mu.Lock(); defer client.mu.Unlock(); return client.calls > 0 })
	time.Sleep(200 * time.Millisecond)
	list, err := store.List(memory.ListArgs{Scope: "project", Workdir: workdir})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range list.Entries {
		if e.Slug == "reply-style" && e.Scope == "project" {
			t.Fatalf("weak new project entry was created because a global entry shares its slug: %+v", e)
		}
	}
}

// scriptedPlanClient answers call i with payloads[i]; "" submits nothing.
type scriptedPlanClient struct {
	fixtureClient
	payloads []string
}

func (c *scriptedPlanClient) Clone() ai.Client { return c }

func (c *scriptedPlanClient) Stream(_ context.Context, _ ai.Request, _ func(string), _ func(string), onCall func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	i := c.calls
	c.calls++
	c.mu.Unlock()
	if i < len(c.payloads) && c.payloads[i] != "" {
		onCall("1", "SubmitMemoryPlan", c.payloads[i])
	}
	return ai.Message{Role: "assistant"}, ai.Usage{}, nil
}

// A malformed plan is retried once with a nudge. When the nudged answer
// submits nothing, extract nudges a second time.
func TestMalformedPlanIsNudgedOnlyOnce(t *testing.T) {
	client := &scriptedPlanClient{payloads: []string{`{"decisions":[{"action":"bogus","slug":"x"}]}`}}
	rt, _ := testRuntime(t, client)
	t.Cleanup(func() { _ = rt.Close() })
	rt.Completed(context.Background(), extractionTurn("I prefer pnpm"))
	waitFor(t, func() bool { client.mu.Lock(); defer client.mu.Unlock(); return client.calls >= 2 })
	time.Sleep(200 * time.Millisecond)
	client.mu.Lock()
	calls := client.calls
	client.mu.Unlock()
	if calls != 2 {
		t.Fatalf("model calls = %d, want 2 (original + one nudge)", calls)
	}
}
