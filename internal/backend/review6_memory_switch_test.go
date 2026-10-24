package backend

// Sixth independent review of the memory master switch (through 5ad877f, goal text
// written while off). Reuses newReviewMemoryFixture and the review2 helpers (real
// backend, real memoryruntime, real session store); LIVEAGENT_HOME is isolated.
//
// "Not extracted" is never asserted after a fixed sleep: every leak check first waits
// for a later sentinel turn of the same session to reach the extractor, which runs the
// session's turns in order.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/goalloop"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func (f *reviewMemoryFixture) review6WaitJudged(t *testing.T, marker string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, request := range f.extractionRequests() {
			for _, m := range request {
				if strings.Contains(m.TextContent(), marker) {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("extractor never judged %q", marker)
}

// review6Sentinel runs one plain turn while memory is on and waits until the extractor
// has judged it, so every earlier turn of the session has been judged or dropped too.
func (f *reviewMemoryFixture) review6Sentinel(t *testing.T, sessionID, requestID string) {
	t.Helper()
	marker := "SENTINEL-" + requestID + " I prefer tabs over spaces"
	accepted := runRequest(t, f.url, sessionID, requestID, marker)
	waitForRunTerminal(t, f.url, sessionID, accepted.RunID)
	f.review6WaitJudged(t, "SENTINEL-"+requestID)
}

func review6Goal(t *testing.T, base, id string, in protocol.GoalRequest) protocol.GoalState {
	t.Helper()
	status, state := postGoal(t, base, id, in)
	if status != http.StatusOK {
		t.Fatalf("goal %s status %d", in.Action, status)
	}
	return state
}

func review6WaitPaused(t *testing.T, base, id string) {
	t.Helper()
	state := waitGoal(t, base, id, func(s protocol.GoalState) bool { return s.Status == "paused" })
	if state.Reason != "max_rounds" {
		t.Fatalf("fixture: goal paused for %q", state.Reason)
	}
}

func review6Meta(t *testing.T, root, id string) session.Meta {
	t.Helper()
	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	meta, _, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	return meta
}

// 验证（预期通过）：开启时设定目标、关闭时 update 改写 → 新原文带标记；重新打开后继续，
// "[goal 1/1] <关闭期间改写的原文>" 不进入提取。
func TestReview6GoalUpdatedWhileOffStaysOutOfExtraction(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	cfg := reviewFixtureConfig()
	cfg.GoalMaxRounds = 1
	f := newReviewMemoryFixture(t, t.TempDir(), cfg)
	sess := createTestSession(t, f.url)
	const secret = "OFFSECRET-UPDATE6-4411 I always deploy on Fridays"

	review6Goal(t, f.url, sess.ID, protocol.GoalRequest{Action: "set", Goal: "tidy the readme", Run: &protocol.PromptRequest{ClientRequestID: "goal-on"}})
	review6WaitPaused(t, f.url, sess.ID)
	f.setEnabled(t, false)
	if state := review6Goal(t, f.url, sess.ID, protocol.GoalRequest{Action: "update", Goal: secret}); state.Goal != secret || state.Status != "paused" {
		t.Fatalf("update: %+v", state)
	}
	f.setEnabled(t, true)
	review6Goal(t, f.url, sess.ID, protocol.GoalRequest{Action: "resume", Run: &protocol.PromptRequest{ClientRequestID: "goal-resume"}})
	review6WaitPaused(t, f.url, sess.ID)
	f.review6Sentinel(t, sess.ID, "s1")
	review2AssertNoLeak(t, f.extractionRequests(), "OFFSECRET-UPDATE6-4411")
}

// 验证（预期通过，设计如此）：关闭时设定、开启时 update → 标记保持（粘性），改写后的原文也不提取。
func TestReview6GoalSetOffUpdatedOnKeepsMark(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	cfg := reviewFixtureConfig()
	cfg.GoalMaxRounds = 1
	root := t.TempDir()
	f := newReviewMemoryFixture(t, root, cfg)
	sess := createTestSession(t, f.url)

	f.setEnabled(t, false)
	review6Goal(t, f.url, sess.ID, protocol.GoalRequest{Action: "set", Goal: "OFFSECRET-SETOFF6-1 first text", Run: &protocol.PromptRequest{ClientRequestID: "goal-off"}})
	review6WaitPaused(t, f.url, sess.ID)
	f.setEnabled(t, true)
	review6Goal(t, f.url, sess.ID, protocol.GoalRequest{Action: "update", Goal: "STICKY6-B second text"})
	review6Goal(t, f.url, sess.ID, protocol.GoalRequest{Action: "resume", Run: &protocol.PromptRequest{ClientRequestID: "goal-resume"}})
	review6WaitPaused(t, f.url, sess.ID)
	f.review6Sentinel(t, sess.ID, "s1")
	requests := f.extractionRequests()
	review2AssertNoLeak(t, requests, "OFFSECRET-SETOFF6-1")
	review2AssertNoLeak(t, requests, "STICKY6-B")
	f.close()
	if meta := review6Meta(t, root, sess.ID); !meta.GoalMemoryOff || meta.Goal != "STICKY6-B second text" {
		t.Fatalf("persisted goal meta: goal=%q off=%v", meta.Goal, meta.GoalMemoryOff)
	}
}

// 验证（预期通过）：关闭时的旧目标被开启时设定的新目标覆盖，标记不继承，新目标的回合照常提取。
func TestReview6NewGoalDoesNotInheritOffMark(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	cfg := reviewFixtureConfig()
	cfg.GoalMaxRounds = 1
	root := t.TempDir()
	f := newReviewMemoryFixture(t, root, cfg)
	sess := createTestSession(t, f.url)

	f.setEnabled(t, false)
	review6Goal(t, f.url, sess.ID, protocol.GoalRequest{Action: "set", Goal: "OFFSECRET-OLD6-77 old goal", Run: &protocol.PromptRequest{ClientRequestID: "goal-off"}})
	review6WaitPaused(t, f.url, sess.ID)
	f.setEnabled(t, true)
	review6Goal(t, f.url, sess.ID, protocol.GoalRequest{Action: "set", Goal: "NEWGOAL6-ON write the changelog", Run: &protocol.PromptRequest{ClientRequestID: "goal-on"}})
	review6WaitPaused(t, f.url, sess.ID)
	f.review6WaitJudged(t, "[goal 1/1] NEWGOAL6-ON")
	f.review6Sentinel(t, sess.ID, "s1")
	review2AssertNoLeak(t, f.extractionRequests(), "OFFSECRET-OLD6-77")
	f.close()
	if meta := review6Meta(t, root, sess.ID); meta.GoalMemoryOff {
		t.Fatalf("new goal set while on inherited the off mark: %+v", meta)
	}
}

// 验证（预期通过）：clear 与 met 都清掉持久化标记；met 之后在开启时设新目标不带标记。
func TestReview6GoalClearAndMetDropMark(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	cfg := reviewFixtureConfig()
	cfg.GoalMaxRounds = 1
	root := t.TempDir()
	f := newReviewMemoryFixture(t, root, cfg)
	cleared, met := createTestSession(t, f.url), createTestSession(t, f.url)

	f.setEnabled(t, false)
	review6Goal(t, f.url, cleared.ID, protocol.GoalRequest{Action: "set", Goal: "clear me", Run: &protocol.PromptRequest{ClientRequestID: "goal-c"}})
	review6WaitPaused(t, f.url, cleared.ID)
	review6Goal(t, f.url, cleared.ID, protocol.GoalRequest{Action: "clear"})

	f.client.scriptedClient.mu.Lock()
	f.client.scriptedClient.response = goalloop.MetToken
	f.client.scriptedClient.mu.Unlock()
	review6Goal(t, f.url, met.ID, protocol.GoalRequest{Action: "set", Goal: "meet me", Run: &protocol.PromptRequest{ClientRequestID: "goal-m"}})
	waitGoal(t, f.url, met.ID, func(s protocol.GoalState) bool {
		return s.Status == "none" || s.Status == "met" || (s.Status == "paused" && s.Goal == "")
	})
	f.client.scriptedClient.mu.Lock()
	f.client.scriptedClient.response = "noted"
	f.client.scriptedClient.mu.Unlock()
	f.setEnabled(t, true)
	review6Goal(t, f.url, met.ID, protocol.GoalRequest{Action: "set", Goal: "NEWAFTERMET6 next goal", Run: &protocol.PromptRequest{ClientRequestID: "goal-m2"}})
	review6WaitPaused(t, f.url, met.ID)
	f.review6WaitJudged(t, "[goal 1/1] NEWAFTERMET6")
	f.close()
	if meta := review6Meta(t, root, cleared.ID); meta.Goal != "" || meta.GoalMemoryOff {
		t.Fatalf("clear kept goal meta: %+v", meta)
	}
	if meta := review6Meta(t, root, met.ID); meta.GoalMemoryOff {
		t.Fatalf("goal set after a met off goal carries the off mark: %+v", meta)
	}
}

// 验证（预期通过）：关闭时设定的目标在后端重启后以 paused/restored 恢复，标记经
// Meta.GoalMemoryOff 往返；开启后 resume，续跑回合不提取目标原文。
func TestReview6RestoredGoalAfterRestartKeepsMark(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	cfg := reviewFixtureConfig()
	cfg.GoalMaxRounds = 1
	root := t.TempDir()
	f := newReviewMemoryFixture(t, root, cfg)
	sess := createTestSession(t, f.url)
	const secret = "OFFSECRET-RESTART6-9090 I always deploy on Fridays"

	f.setEnabled(t, false)
	review6Goal(t, f.url, sess.ID, protocol.GoalRequest{Action: "set", Goal: secret, Run: &protocol.PromptRequest{ClientRequestID: "goal-off"}})
	review6WaitPaused(t, f.url, sess.ID)
	f.close()
	if meta := review6Meta(t, root, sess.ID); !meta.GoalMemoryOff || meta.Goal != secret {
		t.Fatalf("persisted goal meta: %+v", meta)
	}

	f2 := newReviewMemoryFixture(t, root, nil)
	if state := getGoal(t, f2.url, sess.ID); state.Status != "paused" || state.Reason != "restored" {
		t.Fatalf("restored goal: %+v", state)
	}
	f2.setEnabled(t, true)
	review6Goal(t, f2.url, sess.ID, protocol.GoalRequest{Action: "resume", Run: &protocol.PromptRequest{ClientRequestID: "goal-resume"}})
	review6WaitPaused(t, f2.url, sess.ID)
	f2.review6Sentinel(t, sess.ID, "s1")
	review2AssertNoLeak(t, f2.extractionRequests(), "OFFSECRET-RESTART6-9090")
}

// 缺陷：会话分叉（session.Store.Fork，TUI /fork 与 /fork-at 使用）复制了 Goal 却丢了
// GoalMemoryOff。关闭期间设定的目标在分叉会话里变成"开启时写的"，在桌面/WebUI 打开分叉会话、
// 开启记忆后 resume，续跑回合 "[goal 1/1] <原文>" 进入提取。
func TestReview6ForkedGoalLosesOffMark(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	cfg := reviewFixtureConfig()
	cfg.GoalMaxRounds = 1
	root := t.TempDir()
	f := newReviewMemoryFixture(t, root, cfg)
	sess := createTestSession(t, f.url)
	const secret = "OFFSECRET-FORK6-3131 I always deploy on Fridays"

	f.setEnabled(t, false)
	review6Goal(t, f.url, sess.ID, protocol.GoalRequest{Action: "set", Goal: secret, Run: &protocol.PromptRequest{ClientRequestID: "goal-off"}})
	review6WaitPaused(t, f.url, sess.ID)
	f.close()

	// What the TUI's /fork does against the shared session store.
	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	forkID, err := store.Fork(sess.ID, 1<<30, "fork")
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err := store.Load(forkID)
	_ = store.Close()
	if err != nil {
		t.Fatal(err)
	}
	if meta.Goal != secret {
		t.Fatalf("fixture: fork did not copy the goal: %+v", meta)
	}

	f2 := newReviewMemoryFixture(t, root, nil)
	f2.setEnabled(t, true)
	review6Goal(t, f2.url, forkID, protocol.GoalRequest{Action: "resume", Run: &protocol.PromptRequest{ClientRequestID: "goal-resume"}})
	review6WaitPaused(t, f2.url, forkID)
	f2.review6Sentinel(t, forkID, "s1")
	review2AssertNoLeak(t, f2.extractionRequests(), "OFFSECRET-FORK6-3131")
}

// 判断用例：关闭期间用户经 PUT /v1/cron 创建定时 prompt（用户亲手写的原文），重新打开后
// run-now；定时会话里这条 prompt 作为用户消息进入提取。
func TestReview6CronPromptWrittenWhileOffIsExtractedAfterReenable(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	root := t.TempDir()
	f := newReviewMemoryFixture(t, root, reviewFixtureConfig())
	const secret = "OFFSECRET-CRON6-5252 I always deploy on Fridays"

	f.setEnabled(t, false)
	create := CronApplyInput{Ops: []CronOperation{{Op: "create", Item: map[string]any{
		"id": "off-cron", "name": "off-cron", "cron": "0 0 0 1 1 0", "enabled": false, "type": "prompt",
		"prompt": secret, "selectedModel": CronModelRef{CustomProviderID: "fixture", Model: "fixture-model"},
		"workdir": root, "timeoutSeconds": 30,
	}}}}
	if status := doCronJSON(t, f.url+"/v1/cron", http.MethodPut, create, nil); status != http.StatusOK {
		t.Fatalf("create cron status %d", status)
	}
	f.setEnabled(t, true)
	var accepted CronRunNowResponse
	if status := doCronJSON(t, f.url+"/v1/cron/off-cron/run-now", http.MethodPost, nil, &accepted); status != http.StatusAccepted {
		t.Fatalf("run-now status %d", status)
	}
	runs := waitCronRuns(t, f.url, "off-cron", func(runs []CronRunRecord) bool { return len(runs) == 1 && runs[0].State == "done" })
	if !runs[0].Success || runs[0].SessionID == "" {
		t.Fatalf("fixture: cron run %+v", runs[0])
	}
	f.review6Sentinel(t, runs[0].SessionID, "s1")
	review2AssertNoLeak(t, f.extractionRequests(), "OFFSECRET-CRON6-5252")
}

// 并发（-race）：目标持续续跑，同时并发切换开关、并发 update 目标文本。不应有数据竞争、
// 死锁；结束后关闭期间 update 必带持久化标记。
func TestReview6GoalRoundsRaceSettingsAndUpdates(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	cfg := reviewFixtureConfig()
	cfg.GoalMaxRounds = 40
	root := t.TempDir()
	f := newReviewMemoryFixture(t, root, cfg)
	sess := createTestSession(t, f.url)
	review6Goal(t, f.url, sess.ID, protocol.GoalRequest{Action: "set", Goal: "race goal 0", Run: &protocol.PromptRequest{ClientRequestID: "goal-race"}})

	send := func(method, url string, body any) (int, error) {
		raw, _ := json.Marshal(body)
		req, err := http.NewRequest(method, url, bytes.NewReader(raw))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		_ = resp.Body.Close()
		return resp.StatusCode, nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			if code, err := send(http.MethodPut, f.url+"/v1/settings", map[string]any{"memory": map[string]any{"enabled": i%2 == 1}}); err != nil || code != http.StatusOK {
				errs <- fmt.Errorf("toggle %d: %d %v", i, code, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 1; i <= 30; i++ {
			code, err := send(http.MethodPost, f.url+"/v1/sessions/"+sess.ID+"/goal", protocol.GoalRequest{Action: "update", Goal: fmt.Sprintf("race goal %d", i)})
			if err != nil || (code != http.StatusOK && code != http.StatusConflict) {
				errs <- fmt.Errorf("update %d: %d %v", i, code, err)
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	state := getGoal(t, f.url, sess.ID)
	if state.Status == "active" || state.Status == "continuing" {
		_, _ = postGoal(t, f.url, sess.ID, protocol.GoalRequest{Action: "pause"})
	}
	waitGoal(t, f.url, sess.ID, func(s protocol.GoalState) bool { return s.Status == "paused" })
	f.setEnabled(t, false)
	review6Goal(t, f.url, sess.ID, protocol.GoalRequest{Action: "update", Goal: "final off text"})
	f.close()
	if meta := review6Meta(t, root, sess.ID); !meta.GoalMemoryOff {
		t.Fatalf("update while off after the race did not persist the mark: %+v", meta)
	}
}

// 缺陷（同一根因，第二个入口）：TUI 在记忆关闭（启动时读到 memory.enabled=false）时 /goal <原文>，
// 走 tui.setGoal → session.Store.SetGoal，不记录 GoalMemoryOff。之后在桌面/WebUI 打开该会话、
// 开启记忆后 resume，续跑回合把这段关闭期间写的目标原文交给提取器。
func TestReview6TUIGoalWrittenWhileOffHasNoMark(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	cfg := reviewFixtureConfig()
	cfg.GoalMaxRounds = 1
	root := t.TempDir()
	f := newReviewMemoryFixture(t, root, cfg)
	sess := createTestSession(t, f.url)
	f.setEnabled(t, false) // persisted: a TUI started now reads memory.enabled=false
	f.close()
	const secret = "OFFSECRET-TUI6-8080 I always deploy on Fridays"

	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	// tui.setGoal while memory is off.
	if err := store.SetGoalMemory(sess.ID, secret, true); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	f2 := newReviewMemoryFixture(t, root, nil)
	f2.setEnabled(t, true)
	review6Goal(t, f2.url, sess.ID, protocol.GoalRequest{Action: "resume", Run: &protocol.PromptRequest{ClientRequestID: "goal-resume"}})
	review6WaitPaused(t, f2.url, sess.ID)
	f2.review6Sentinel(t, sess.ID, "s1")
	review2AssertNoLeak(t, f2.extractionRequests(), "OFFSECRET-TUI6-8080")
}
