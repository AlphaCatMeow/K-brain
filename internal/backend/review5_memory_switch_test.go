package backend

// Fifth independent review of the memory master switch (through b3f0584). Reuses
// newReviewMemoryFixture and the review2/review3 helpers (real backend, real
// memoryruntime, real session store); LIVEAGENT_HOME and the data root are isolated.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func review5LastState(text string) string {
	off, on := strings.LastIndex(text, "Memory is turned off"), strings.LastIndex(text, "User prefers Go for services")
	switch {
	case off < 0 && on < 0:
		return "none"
	case off > on:
		return "off"
	default:
		return "on"
	}
}

// 关闭期间设定 /goal，回合在关闭期间跑到轮数上限暂停；用户打开记忆后继续目标。
// 继续之后后端自动发起的下一轮，用户消息是 "[goal 1/2] <目标原文>"，没有 memory-off 标记，
// 回合完成后目标原文作为 latest-user-message 交给了提取器。
func TestReview5MemorySwitchGoalSetWhileOffIsExtractedOnLaterRounds(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	cfg := reviewFixtureConfig()
	cfg.GoalMaxRounds = 2
	f := newReviewMemoryFixture(t, t.TempDir(), cfg)
	sess := createTestSession(t, f.url)
	const secret = "OFFSECRET-GOAL-5150 I always deploy on Fridays"

	f.setEnabled(t, false)
	if status, _ := postGoal(t, f.url, sess.ID, protocol.GoalRequest{Action: "set", Goal: secret, Run: &protocol.PromptRequest{ClientRequestID: "goal-off"}}); status != http.StatusOK {
		t.Fatalf("set goal status %d", status)
	}
	paused := waitGoal(t, f.url, sess.ID, func(s protocol.GoalState) bool { return s.Status == "paused" })
	if paused.Reason != "max_rounds" {
		t.Fatalf("fixture: goal paused for %q", paused.Reason)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(f.extractionRequests()); n != 0 {
		t.Fatalf("extractor ran while memory is off: %d", n)
	}

	f.setEnabled(t, true)
	if status, _ := postGoal(t, f.url, sess.ID, protocol.GoalRequest{Action: "resume", Run: &protocol.PromptRequest{ClientRequestID: "goal-resume"}}); status != http.StatusOK {
		t.Fatalf("resume goal status %d", status)
	}
	waitGoal(t, f.url, sess.ID, func(s protocol.GoalState) bool { return s.Status == "paused" })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(f.extractionRequests()) < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	for _, m := range getSession(t, f.url, sess.ID).Messages {
		if m.Role == protocol.RoleUser && len(m.Content) > 0 && strings.Contains(m.Content[0].Text, "OFFSECRET-GOAL-5150") {
			t.Logf("history user message: %q", m.Content[0].Text)
		}
	}
	review2AssertNoLeak(t, f.review2WaitExtraction(t), "OFFSECRET-GOAL-5150")
}

// 验证（预期通过）：同一条尾消息 关→停→开→恢复停→关→恢复停→开→恢复停→关→恢复完成，
// 每一步请求里最后的记忆状态与开关一致，撤回/注入各只在状态翻转时出现一次，工具随开关。
func TestReview5MemorySwitchResumeAcrossManyTogglesFollowsSwitch(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	f := newReviewMemoryFixture(t, t.TempDir(), reviewFixtureConfig())
	sess := createTestSession(t, f.url)

	seq := f.turn(t, sess.ID, "on-1", "hello there friend", 0)
	f.setEnabled(t, false)
	seq = f.review3CancelledTurn(t, sess.ID, "off-0", "please look at the build script", seq)
	offs, ons := 1, 1
	check := func(step string, enabled bool) {
		t.Helper()
		text := f.review3LastRequestText()
		want := "off"
		if enabled {
			want = "on"
		}
		if got := review5LastState(text); got != want {
			t.Fatalf("%s: last memory state %s, switch %s:\n%s", step, got, want, text)
		}
		if n := strings.Count(text, "Memory is turned off"); n != offs {
			t.Fatalf("%s: want %d retractions, got %d:\n%s", step, offs, n, text)
		}
		if n := strings.Count(text, "User prefers Go for services"); n != ons {
			t.Fatalf("%s: want %d memory injections, got %d:\n%s", step, ons, n, text)
		}
		tools, _ := f.client.lastRequest(t)
		if slices.Contains(tools, "memory_manager") != enabled {
			t.Fatalf("%s: memory_manager offered=%v with switch %v", step, !enabled, enabled)
		}
	}
	check("off-0", false)
	for i, enabled := range []bool{true, false, true, false, true} {
		f.setEnabled(t, enabled)
		if enabled {
			ons++
		} else {
			offs++
		}
		step := "resume-" + string(rune('a'+i))
		seq = f.review3CancelledTurnResume(t, sess.ID, step, seq)
		check(step, enabled)
		// Resuming twice in the same state adds nothing.
		seq = f.review3CancelledTurnResume(t, sess.ID, step+"-again", seq)
		check(step+"-again", enabled)
	}
	f.setEnabled(t, false)
	offs++
	f.review3Resume(t, sess.ID, "final-off", seq)
	check("final-off", false)
}

// 验证（预期通过）：关闭期间入队一条、开启后入队一条，暂停的队列在开启后用 move、
// edit_begin/edit_commit（开启时编辑）、重启后端、run_now 处理。关闭期间那条始终不进入提取，
// 开启时那条照常被提取（WithMemoryOffAuthored 不会泄漏到队列下一项）。
func TestReview5MemorySwitchQueuedOffSurvivesEditsMoveRestartAndRunNow(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	root := t.TempDir()
	cfg := reviewFixtureConfig()
	if err := cfg.SaveFile(filepath.Join(root, "config.json")); err != nil {
		t.Fatal(err)
	}
	f := newReviewMemoryFixture(t, root, nil)
	sess := createTestSession(t, f.url)
	queueURL := f.url + "/v1/sessions/" + sess.ID + "/queue"
	const offSecret = "OFFSECRET-QUEUE5-7781 I always deploy on Fridays"
	const onText = "ONTEXT-QUEUE5-1203 I prefer short commit messages"

	paused := queueTestAction(t, queueURL, "pause", "", 0)
	_ = paused
	f.setEnabled(t, false)
	off := queueTestSubmit(t, queueURL, "q-off", offSecret)
	f.setEnabled(t, true)
	on := queueTestSubmit(t, queueURL, "q-on", onText)

	// move the on item ahead, then edit the off item while memory is on.
	moved := queueTestGet(t, queueURL)
	var out queueResponse
	if code := doJSON(t, http.MethodPost, queueURL+"/actions", queueAction{Action: "move", ItemID: on.Receipt.ID, Direction: "up", Revision: moved.Snapshot.Revision}, &out); code != 200 {
		t.Fatalf("move=%d", code)
	}
	edit := queueTestAction(t, queueURL, "edit_begin", off.Receipt.ID, out.Snapshot.Revision)
	req := *edit.Item.Request
	req.Prompt = offSecret + " (edited)"
	if code := doJSON(t, http.MethodPost, queueURL+"/actions", queueAction{Action: "edit_commit", ItemID: off.Receipt.ID, Revision: edit.Snapshot.Revision, Request: &req}, &out); code != 200 {
		t.Fatalf("edit_commit=%d %+v", code, out)
	}
	edit = queueTestAction(t, queueURL, "edit_begin", off.Receipt.ID, out.Snapshot.Revision)
	queueTestAction(t, queueURL, "edit_cancel", off.Receipt.ID, edit.Snapshot.Revision)

	// Restart: the queue file is the only carrier of memoryOff.
	f.close()
	data, err := os.ReadFile(filepath.Join(root, "events", sess.ID+".queue.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc queueDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	for _, item := range doc.Items {
		if item.MemoryOff != (item.Request.ClientRequestID == "q-off") {
			t.Fatalf("persisted memoryOff=%v for %s", item.MemoryOff, item.Request.ClientRequestID)
		}
	}
	f2 := newReviewMemoryFixture(t, root, nil)
	queueURL = f2.url + "/v1/sessions/" + sess.ID + "/queue"
	current := queueTestGet(t, queueURL)
	queueTestAction(t, queueURL, "run_now", off.Receipt.ID, current.Snapshot.Revision)
	queueTestWait(t, queueURL, func(out queueResponse) bool { return len(out.Snapshot.Items) == 0 })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		judged := false
		for _, request := range f2.extractionRequests() {
			for _, m := range request {
				judged = judged || strings.Contains(m.TextContent(), "ONTEXT-QUEUE5-1203")
			}
		}
		if judged {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	requests := f2.review2WaitExtraction(t)
	review2AssertNoLeak(t, requests, "OFFSECRET-QUEUE5-7781")
	found := false
	for _, request := range requests {
		for _, m := range request {
			found = found || strings.Contains(m.TextContent(), "<latest-user-message>\n"+onText)
		}
	}
	if !found {
		t.Fatalf("the item queued while on was not judged; the off mark leaked to the next queue item")
	}
}

// 验证（预期通过）：旧格式队列条目（无 memoryOff 字段）解码为 false；cloneQueue 保留字段。
func TestReview5MemorySwitchQueueEntryCompat(t *testing.T) {
	var entry queueEntry
	if err := json.Unmarshal([]byte(`{"id":"a","request":{"client_request_id":"x","prompt":"p"},"source":"","createdAt":1,"draftJson":"","uploadedFilesJson":"[]"}`), &entry); err != nil || entry.MemoryOff {
		t.Fatalf("old entry: %+v %v", entry, err)
	}
	doc := &queueDocument{Version: 1, Items: []queueEntry{{ID: "a", MemoryOff: true}}, Edits: map[string]queueEdit{"b": {Entry: queueEntry{ID: "b", MemoryOff: true}}}, Receipts: map[string]queueReceipt{}}
	copy := cloneQueue(doc)
	if !copy.Items[0].MemoryOff || !copy.Edits["b"].Entry.MemoryOff {
		t.Fatalf("cloneQueue dropped memoryOff: %+v", copy)
	}
}

// 验证（预期通过）：没有设置存储时（嵌入式/测试用法），入队按会话代理的开关记录 memoryOff。
func TestReview5MemorySwitchQueueWithoutSettingsUsesAgentSwitch(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eventDir := t.TempDir()
	client := &scriptedClient{response: "ok"}
	server, err := New(Options{
		Store: store, EventDir: eventDir, DefaultCWD: t.TempDir(),
		Models: []protocol.ModelRef{{Provider: "fixture", Model: "fixture-model"}},
		Factory: func(_ context.Context, cwd string, selected protocol.ModelRef) (*agent.Agent, error) {
			ag := agent.New(client, selected.Model, 1024, "system", agent.WithMemoryEnabled(false))
			ag.WorkingDir, ag.ModelName, ag.Provider = cwd, selected.Model, selected.Provider
			return ag, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() { httpServer.Close(); _ = server.Close() })
	h := httpServer.URL
	sess := createTestSession(t, h)
	queueURL := h + "/v1/sessions/" + sess.ID + "/queue"
	queueTestAction(t, queueURL, "pause", "", 0)
	queueTestSubmit(t, queueURL, "q", "hello")
	data, err := os.ReadFile(filepath.Join(eventDir, sess.ID+".queue.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"memoryOff":true`) {
		t.Fatalf("queued while the agent switch is off, memoryOff not recorded: %s", data)
	}
}
