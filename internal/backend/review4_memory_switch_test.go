package backend

// Fourth independent review of the memory master switch (through cb55ec6). Reuses
// newReviewMemoryFixture and the review2/review3 helpers (real backend, real
// memoryruntime, real session store); LIVEAGENT_HOME and the data root are isolated.

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// 关→停止→开→恢复（又停止）→关→恢复，同一条尾消息。开启时的恢复给它追加了新的记忆快照，
// 但 memory-off 标记保留；再次关闭时 markResumedMessage 看到标记就提前返回，不写撤回。
// 于是关闭状态下发给对话模型的请求，最后一份记忆状态是开启时的记忆条目。
func TestReview4MemorySwitchOffOnOffResumeEndsOnRestoredMemory(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	f := newReviewMemoryFixture(t, t.TempDir(), reviewFixtureConfig())
	sess := createTestSession(t, f.url)

	seq := f.turn(t, sess.ID, "on-1", "hello there friend", 0)
	f.setEnabled(t, false)
	seq = f.review3CancelledTurn(t, sess.ID, "off-1", "please look at the build script", seq)
	f.setEnabled(t, true)
	seq = f.review3CancelledTurnResume(t, sess.ID, "on-resume", seq)
	if text := f.review3LastRequestText(); strings.LastIndex(text, "Memory is turned off") > strings.LastIndex(text, "prefers-go") {
		t.Fatalf("precondition: resume while on must restore memory:\n%s", text)
	}
	f.setEnabled(t, false)
	f.review3Resume(t, sess.ID, "off-resume", seq)

	tools, _ := f.client.lastRequest(t)
	if slices.Contains(tools, "memory_manager") {
		t.Fatalf("memory tool offered while off: %v", tools)
	}
	text := f.review3LastRequestText()
	if off, mem := strings.LastIndex(text, "Memory is turned off"), strings.LastIndex(text, "prefers-go"); mem > off {
		t.Fatalf("memory is off (tools=%v) but the resumed request ends on the saved memory restored by the earlier on-resume, with no retraction after it (off notice at %d, memory at %d):\n%s", tools, off, mem, text)
	}
}

// 关闭期间用户把一条消息放进队列（当前有回合在跑），随后打开记忆，再停止当前回合；
// 队列在开启状态下执行这条关闭期间写下的消息，提取器拿到了原话。
func TestReview4MemorySwitchQueuedWhileOffDispatchedAfterReenableIsExtracted(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	f := newReviewMemoryFixture(t, t.TempDir(), reviewFixtureConfig())
	sess := createTestSession(t, f.url)
	const secret = "OFFSECRET-QUEUE-4410 I always deploy on Fridays"
	queueURL := f.url + "/v1/sessions/" + sess.ID + "/queue"

	f.setEnabled(t, false)
	f.client.mu.Lock()
	before := len(f.client.tools)
	f.client.mu.Unlock()
	f.client.scriptedClient.mu.Lock()
	f.client.scriptedClient.wait = true
	f.client.scriptedClient.mu.Unlock()
	active := runRequest(t, f.url, sess.ID, "off-active", "working on the build while off")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.client.mu.Lock()
		called := len(f.client.tools) > before
		f.client.mu.Unlock()
		if called {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	queueTestSubmit(t, queueURL, "off-queued", secret)

	f.setEnabled(t, true)
	f.client.scriptedClient.mu.Lock()
	f.client.scriptedClient.wait = false
	f.client.scriptedClient.mu.Unlock()
	if code := doJSON(t, http.MethodPost, f.url+"/v1/sessions/"+sess.ID+"/runs/"+active.RunID+"/cancel", map[string]any{}, nil); code >= 300 {
		t.Fatalf("cancel status %d", code)
	}
	queued := queueTestWait(t, queueURL+"?client_request_id=off-queued", func(out queueResponse) bool { return out.Receipt != nil && out.Receipt.Run != nil })
	events := waitForRun(t, f.url, sess.ID, queued.Receipt.Run.AcceptedSeq-1)
	// The queued message itself must not be judged; a later turn written while on runs the
	// extractor, and the queued text must not appear as earlier context either.
	f.turn(t, sess.ID, "on-after", "I also prefer short commit messages", events[len(events)-1].Seq)
	review2AssertNoLeak(t, f.review2WaitExtraction(t), "OFFSECRET-QUEUE-4410")
}

// 验证（预期通过）：关闭期间连续两轮完成的回合 + 关闭期间的 Steer 之后，撤回只出现一次；
// 重新打开后注入一次记忆，不重复。
func TestReview4MemorySwitchSnapshotsDedupAcrossToggles(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	f := newReviewMemoryFixture(t, t.TempDir(), reviewFixtureConfig())
	sess := createTestSession(t, f.url)

	seq := f.turn(t, sess.ID, "on-1", "hello there friend", 0)
	f.setEnabled(t, false)
	seq = f.turn(t, sess.ID, "off-1", "first off message", seq)
	seq = f.turn(t, sess.ID, "off-2", "second off message", seq)
	if n := strings.Count(f.review3LastRequestText(), "Memory is turned off"); n != 1 {
		t.Fatalf("want one retraction across two off turns, got %d", n)
	}
	f.setEnabled(t, true)
	seq = f.turn(t, sess.ID, "on-2", "back on", seq)
	f.turn(t, sess.ID, "on-3", "still on", seq)
	text := f.review3LastRequestText()
	if n := strings.Count(text, "Memory is turned off"); n != 1 {
		t.Fatalf("want the old retraction once, got %d", n)
	}
	// Memory appears in on-1 and once more after re-enabling.
	if n := strings.Count(text, "User prefers Go for services"); n != 2 {
		t.Fatalf("want memory injected twice (on-1 and re-enable), got %d:\n%s", n, text)
	}
}

// 验证（预期通过）：关→停止→开→恢复（完成）→开启下一轮。尾消息被就地追加记忆后，
// 下一轮 retainedSnapshot 读到它，不重复注入记忆索引。
func TestReview4MemorySwitchOnResumeThenNextTurnDoesNotReinject(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	f := newReviewMemoryFixture(t, t.TempDir(), reviewFixtureConfig())
	sess := createTestSession(t, f.url)

	seq := f.turn(t, sess.ID, "on-1", "hello there friend", 0)
	f.setEnabled(t, false)
	seq = f.review3CancelledTurn(t, sess.ID, "off-1", "please look at the build script", seq)
	f.setEnabled(t, true)
	seq = f.review3Resume(t, sess.ID, "on-resume", seq)
	f.turn(t, sess.ID, "on-2", "next while on", seq)
	text := f.review3LastRequestText()
	if n := strings.Count(text, "User prefers Go for services"); n != 2 {
		t.Fatalf("want memory index twice (on-1, on-resume), got %d:\n%s", n, text)
	}
	if n := strings.Count(text, "Memory is turned off"); n != 1 {
		t.Fatalf("want one retraction, got %d", n)
	}
}
