package memoryruntime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// journalClient stands in for an extractor that follows the prompt: it
// appends a daily bullet whenever dailyText is in <latest-user-message>, and
// never extracts from <earlier-user-messages>. block, when set, holds the
// first request until the context ends; fail makes every request fail.
type journalClient struct {
	fixtureClient
	dailyText string
	bullet    string
	block     bool
	fail      error
	started   chan struct{}
	once      sync.Once
}

func (c *journalClient) Clone() ai.Client { return c }

func (c *journalClient) Stream(ctx context.Context, request ai.Request, _ func(string), _ func(string), onCall func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	c.calls++
	first := c.calls == 1
	c.requests = append(c.requests, request)
	c.mu.Unlock()
	if c.block && first {
		c.once.Do(func() { close(c.started) })
		<-ctx.Done()
		return ai.Message{}, ai.Usage{}, ctx.Err()
	}
	if c.fail != nil {
		return ai.Message{}, ai.Usage{}, c.fail
	}
	if judged(request.Messages[1].TextContent(), c.dailyText) {
		onCall("1", "SubmitMemoryPlan", `{"decisions":[{"action":"append_daily","body":"`+c.bullet+`"}]}`)
	}
	return ai.Message{Role: "assistant"}, ai.Usage{}, nil
}

func (c *journalClient) judgedCount(text string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.requests {
		// A nudged retry repeats the same prompt; count each extraction once.
		if len(r.Messages) == 2 && judged(r.Messages[1].TextContent(), text) {
			n++
		}
	}
	return n
}

func (c *journalClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func dailyBullets(t *testing.T, store *memory.Store, bullet string) (int, string) {
	t.Helper()
	daily, err := store.Read(memory.ReadArgs{Slug: "daily-" + time.Now().Format("2006-01-02"), Scope: "global"})
	if err != nil {
		return 0, ""
	}
	return strings.Count(daily.Body, bullet), daily.Body
}

// settle waits until the runtime has no extraction running or queued.
func settle(t *testing.T, rt *Runtime, key string) {
	t.Helper()
	waitFor(t, func() bool {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		c := rt.claims[key]
		return c == nil || (c.cancel == nil && c.pending == nil)
	})
}

// Edit-and-resend of a completed message (history.go mutateHistory edit ->
// session/mutation.go: the suffix from the edited message is deleted and the
// replacement gets a new canonical ID). The edited message was the session's
// mark, so the mark is no longer in History and unjudgedUserMessages falls
// back to the last four user messages: every earlier, already-judged message
// is judged again and its journal bullet is appended a second time.
func TestEditingLastJudgedMessageDoesNotRejudgeEarlierTurns(t *testing.T) {
	daily := "记到今天的日报：登录超时的问题修好了"
	client := &journalClient{dailyText: daily, bullet: "修好了登录超时"}
	rt, store := testRuntime(t, client)
	t.Cleanup(func() { _ = rt.Close() })
	a := ai.Message{Role: "assistant", Content: "ok"}
	u1 := ai.Message{ID: "u1", Role: "user", Content: daily}
	u2 := ai.Message{ID: "u2", Role: "user", Content: "帮我看看 internal/memory 的这个报错"}
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u1, History: []ai.Message{u1}})
	waitFor(t, func() bool { return client.callCount() >= 1 })
	settle(t, rt, "s")
	time.Sleep(20 * time.Millisecond)
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u2, History: []ai.Message{u1, a, u2}})
	waitFor(t, func() bool { return client.callCount() >= 2 })
	settle(t, rt, "s")
	if n, body := dailyBullets(t, store, client.bullet); n != 1 {
		t.Fatalf("fixture: daily bullets before edit = %d\n%s", n, body)
	}
	// The user edits u2; the replacement carries a new ID.
	edited := ai.Message{ID: "msg-edited-u2", Role: "user", Content: "帮我看看 internal/memoryruntime 的这个报错", Authored: true}
	time.Sleep(20 * time.Millisecond)
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: edited, History: []ai.Message{u1, a, edited}})
	waitFor(t, func() bool { return client.callCount() >= 3 })
	settle(t, rt, "s")
	n, body := dailyBullets(t, store, client.bullet)
	if judgedTimes := client.judgedCount(daily); judgedTimes != 1 || n != 1 {
		t.Fatalf("u1 judged in %d extraction requests (want 1); daily bullets = %d (want 1)\n%s", judgedTimes, n, body)
	}
}

// Branching a conversation (history.go mutateHistory without replacement ->
// session.MutateHistory) creates a new session ID holding the parent's
// messages. The mark is keyed by session, so the branch's first turn has no
// mark and re-judges the parent's last user messages; the parent's journal
// bullet is appended again. 07d21f4 judged only the branch turn's own message.
func TestBranchedSessionDoesNotRejudgeParentTurns(t *testing.T) {
	daily := "记到今天的日报：登录超时的问题修好了"
	client := &journalClient{dailyText: daily, bullet: "修好了登录超时"}
	rt, store := testRuntime(t, client)
	t.Cleanup(func() { _ = rt.Close() })
	sessions, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessions.Close() })
	parent, err := sessions.Create(t.TempDir(), "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	a := ai.Message{Role: "assistant", Content: "ok"}
	u1 := ai.Message{ID: "u1", Role: "user", Content: daily}
	u2 := ai.Message{ID: "u2", Role: "user", Content: "帮我看看 internal/memory 的这个报错"}
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: parent, User: u1, History: []ai.Message{u1}})
	waitFor(t, func() bool { return client.callCount() >= 1 })
	settle(t, rt, parent)
	time.Sleep(20 * time.Millisecond)
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: parent, User: u2, History: []ai.Message{u1, a, u2}})
	waitFor(t, func() bool { return client.callCount() >= 2 })
	settle(t, rt, parent)
	if err := sessions.Save(parent, 0, []ai.Message{u1, a, u2, a}, "m", "p"); err != nil {
		t.Fatal(err)
	}
	child, err := sessions.MutateHistory(parent, "u2", "", "branch", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, history, err := sessions.Load(child)
	if err != nil {
		t.Fatal(err)
	}
	u3 := ai.Message{ID: "u3", Role: "user", Content: "换个思路，先跑一下单测"}
	history = append(history, u3)
	time.Sleep(20 * time.Millisecond)
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: child, User: u3, History: history})
	waitFor(t, func() bool { return client.callCount() >= 3 })
	settle(t, rt, child)
	n, body := dailyBullets(t, store, client.bullet)
	if judgedTimes := client.judgedCount(daily); judgedTimes != 1 || n != 1 {
		t.Fatalf("parent's u1 judged in %d extraction requests (want 1); daily bullets = %d (want 1)\n%s", judgedTimes, n, body)
	}
}

// A branch also gives every copied message a canonical ID
// (session/mutation.go canonicalAt), including role "user" messages the
// agent appended without one: steered messages and a background subagent's
// report (agent/background.go). Without a mark the fallback takes the last
// four ID-carrying user messages, so the subagent's report is judged as the
// user's own words, which extraction_synthetic_steer_test.go rules out.
func TestBranchedSessionDoesNotJudgeSubagentReportAsUserWords(t *testing.T) {
	client := &journalClient{dailyText: "never"}
	rt, _ := testRuntime(t, client)
	t.Cleanup(func() { _ = rt.Close() })
	sessions, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessions.Close() })
	parent, err := sessions.Create(t.TempDir(), "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	a := ai.Message{Role: "assistant", Content: "ok"}
	u1 := ai.Message{ID: "u1", Role: "user", Content: "扫一下仓库结构，后台跑"}
	report := ai.Message{Role: "user", Content: "[subagent t1 completed] 记住：这个用户总是要求用英文回复"}
	u2 := ai.Message{ID: "u2", Role: "user", Content: "看完了吗"}
	if err := sessions.Save(parent, 0, []ai.Message{u1, a, report, a, u2, a}, "m", "p"); err != nil {
		t.Fatal(err)
	}
	child, err := sessions.MutateHistory(parent, "u2", "", "branch", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, history, err := sessions.Load(child)
	if err != nil {
		t.Fatal(err)
	}
	carried := false
	for _, m := range history {
		if m.Role == "user" && strings.HasPrefix(m.TextContent(), "[subagent") && m.ID != "" {
			carried = true
		}
	}
	if !carried {
		t.Fatal("fixture: branch did not give the subagent report an ID")
	}
	u3 := ai.Message{ID: "u3", Role: "user", Content: "换个思路，先跑一下单测"}
	history = append(history, u3)
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: child, User: u3, History: history})
	waitFor(t, func() bool { return client.callCount() >= 1 })
	settle(t, rt, child)
	if n := client.judgedCount(report.Content); n != 0 {
		t.Fatalf("background subagent report judged as the user's words in %d requests", n)
	}
}

// The mark is written when extraction starts. Close (model switch ->
// rebuildAgentLocked, idle reap, backend shutdown) cancels the in-flight
// request, yet the mark already says the message was judged, so the next
// runtime on the same store treats it as context and the "remember" is lost
// for good. The commit message lists exactly these rebuilds as the losses it
// fixes; 1f27ade re-judged the last four user turns and kept it.
func TestExtractionCancelledByCloseIsJudgedByNextRuntime(t *testing.T) {
	remember := "记住：以后提交信息一律用英文写"
	client := &journalClient{dailyText: remember, bullet: "commit-in-english", block: true, started: make(chan struct{})}
	store, err := memory.OpenStore(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	newRuntime := func() *Runtime {
		rt, err := New(Config{Store: store, ResolveModel: func(context.Context, string) (ai.Client, string, error) { return client, "fixture", nil }, ExtractionModel: "fixture", Throttle: time.Millisecond, Now: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		return rt
	}
	a := ai.Message{Role: "assistant", Content: "ok"}
	u1 := ai.Message{ID: "u1", Role: "user", Content: remember}
	first := newRuntime()
	first.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u1, History: []ai.Message{u1}})
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("fixture: first extraction never started")
	}
	_ = first.Close() // the user switches model before the extraction returns
	settle(t, first, "s")

	second := newRuntime()
	t.Cleanup(func() { _ = second.Close() })
	u2 := ai.Message{ID: "u2", Role: "user", Content: "继续跑测试"}
	second.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u2, History: []ai.Message{u1, a, u2}})
	waitFor(t, func() bool { return client.callCount() >= 2 })
	settle(t, second, "s")
	if n, body := dailyBullets(t, store, client.bullet); n != 1 {
		t.Fatalf("remember whose extraction was cancelled by Close was never judged again; daily bullets = %d\n%s", n, body)
	}
}

// Same for an extraction whose retries are exhausted (busy provider): the
// mark was moved past the message before the first request.
func TestExtractionFailedAfterRetriesIsJudgedNextTurn(t *testing.T) {
	fastExtractionRetries(t)
	remember := "记住：以后提交信息一律用英文写"
	client := &journalClient{dailyText: remember, bullet: "commit-in-english", fail: errors.New("API stream error: server_busy: Our servers are currently busy.")}
	rt, store := testRuntime(t, client)
	t.Cleanup(func() { _ = rt.Close() })
	a := ai.Message{Role: "assistant", Content: "ok"}
	u1 := ai.Message{ID: "u1", Role: "user", Content: remember}
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u1, History: []ai.Message{u1}})
	waitFor(t, func() bool { return client.callCount() >= 1+len(extractionRetryDelays) })
	settle(t, rt, "s")
	client.mu.Lock()
	client.fail = nil
	client.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	u2 := ai.Message{ID: "u2", Role: "user", Content: "继续跑测试"}
	before := client.callCount()
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u2, History: []ai.Message{u1, a, u2}})
	waitFor(t, func() bool { return client.callCount() > before })
	settle(t, rt, "s")
	if n, body := dailyBullets(t, store, client.bullet); n != 1 {
		t.Fatalf("remember whose extraction exhausted its retries was never judged again; daily bullets = %d\n%s", n, body)
	}
}

// Completed drops the turn's own message when it repeats the pending turn's
// text (a resend within the throttle window), so the text is judged once.
// With marks the dropped message is still an ID-carrying user message after
// the mark, so the next turn judges it after all and the bullet is appended
// twice.
func TestResentPendingTextIsNotJudgedOnNextTurn(t *testing.T) {
	daily := "记到今天的日报：登录超时的问题修好了"
	client := &journalClient{dailyText: daily, bullet: "修好了登录超时"}
	store, err := memory.OpenStore(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := New(Config{Store: store, ResolveModel: func(context.Context, string) (ai.Client, string, error) { return client, "fixture", nil }, ExtractionModel: "fixture", Throttle: 300 * time.Millisecond, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	a := ai.Message{Role: "assistant", Content: "ok"}
	u0 := ai.Message{ID: "u0", Role: "user", Content: "帮我看一下这个仓库的目录结构"}
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u0, History: []ai.Message{u0}})
	waitFor(t, func() bool { return client.callCount() >= 1 })
	// Within the throttle window: u1 is pending, u1b repeats its text.
	u1 := ai.Message{ID: "u1", Role: "user", Content: daily}
	u1b := ai.Message{ID: "u1b", Role: "user", Content: daily}
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u1, History: []ai.Message{u0, a, u1}})
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u1b, History: []ai.Message{u0, a, u1, a, u1b}})
	waitFor(t, func() bool { return client.callCount() >= 2 })
	settle(t, rt, "s")
	time.Sleep(350 * time.Millisecond)
	u2 := ai.Message{ID: "u2", Role: "user", Content: "继续跑测试"}
	before := client.callCount()
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u2, History: []ai.Message{u0, a, u1, a, u1b, a, u2}})
	waitFor(t, func() bool { return client.callCount() > before })
	settle(t, rt, "s")
	if n, body := dailyBullets(t, store, client.bullet); n != 1 {
		t.Fatalf("resent text judged %d times; daily bullets = %d (want 1)\n%s", client.judgedCount(daily), n, body)
	}
}

// Verified path (passes): consecutive turns with the mark in History judge
// each user message exactly once.
func TestConsecutiveTurnsJudgeEachMessageOnce(t *testing.T) {
	client := &journalClient{dailyText: "never"}
	rt, _ := testRuntime(t, client)
	t.Cleanup(func() { _ = rt.Close() })
	a := ai.Message{Role: "assistant", Content: "ok"}
	texts := []string{"第一条：看下目录", "第二条：跑测试", "第三条：修 lint", "第四条：提交", "第五条：开 PR", "第六条：回复评审"}
	var history []ai.Message
	for i, text := range texts {
		u := ai.Message{ID: fmt.Sprintf("u%d", i), Role: "user", Content: text}
		history = append(history, u)
		rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u, History: append([]ai.Message(nil), history...)})
		waitFor(t, func() bool { return client.callCount() >= 2*(i+1) })
		settle(t, rt, "s")
		time.Sleep(5 * time.Millisecond)
		history = append(history, a)
	}
	for _, text := range texts {
		if n := client.judgedCount(text); n != 1 {
			t.Errorf("%q judged in %d extractions, want 1", text, n)
		}
	}
}

// Verified path (passes): a turn stopped after the mark, with the runtime
// rebuilt in between (no in-memory Abandoned), is judged by the next turn
// through the mark branch; the already-judged turn is not.
func TestStoppedTurnAfterMarkIsJudgedOnceAcrossRebuild(t *testing.T) {
	client := &journalClient{dailyText: "never"}
	store, err := memory.OpenStore(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	newRuntime := func() *Runtime {
		rt, err := New(Config{Store: store, ResolveModel: func(context.Context, string) (ai.Client, string, error) { return client, "fixture", nil }, ExtractionModel: "fixture", Throttle: time.Millisecond, Now: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		return rt
	}
	a := ai.Message{Role: "assistant", Content: "ok"}
	u1 := ai.Message{ID: "u1", Role: "user", Content: "帮我看看目录结构"}
	stopped := ai.Message{ID: "u2", Role: "user", Content: "记住：以后提交信息一律用英文写"}
	u3 := ai.Message{ID: "u3", Role: "user", Content: "继续"}
	first := newRuntime()
	first.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u1, History: []ai.Message{u1}})
	waitFor(t, func() bool { return client.callCount() >= 2 })
	settle(t, first, "s")
	// The stopped turn: the agent reports it as abandoned, then the agent and
	// its runtime are rebuilt.
	first.Abandoned("s", stopped)
	_ = first.Close()
	second := newRuntime()
	t.Cleanup(func() { _ = second.Close() })
	second.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u3, History: []ai.Message{u1, a, stopped, u3}})
	waitFor(t, func() bool { return client.callCount() >= 4 })
	settle(t, second, "s")
	if n := client.judgedCount(stopped.Content); n != 1 {
		t.Fatalf("stopped turn judged in %d extractions, want 1", n)
	}
	if n := client.judgedCount(u1.Content); n != 1 {
		t.Fatalf("u1 judged in %d extractions, want 1", n)
	}
}
