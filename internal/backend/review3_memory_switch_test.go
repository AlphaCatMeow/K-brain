package backend

// Third independent review of the memory master switch (8ac064b + bb6d83c + 9f66715).
// Reuses newReviewMemoryFixture (real backend, real memoryruntime, real session store);
// LIVEAGENT_HOME and the data root are isolated under t.TempDir().

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// review3CancelledTurn starts a run that blocks in the conversation model, cancels it
// and returns the last event seq; the tail of History stays the user message.
func (f *reviewMemoryFixture) review3CancelledTurn(t *testing.T, sessionID, requestID, prompt string, after int64) int64 {
	t.Helper()
	f.client.mu.Lock()
	before := len(f.client.tools)
	f.client.mu.Unlock()
	f.client.scriptedClient.mu.Lock()
	f.client.scriptedClient.wait = true
	f.client.scriptedClient.mu.Unlock()
	accepted := runRequest(t, f.url, sessionID, requestID, prompt)
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
	if code := doJSON(t, http.MethodPost, f.url+"/v1/sessions/"+sessionID+"/runs/"+accepted.RunID+"/cancel", map[string]any{}, nil); code >= 300 {
		t.Fatalf("cancel status %d", code)
	}
	events := waitForRun(t, f.url, sessionID, after)
	f.client.scriptedClient.mu.Lock()
	f.client.scriptedClient.wait = false
	f.client.scriptedClient.mu.Unlock()
	return events[len(events)-1].Seq
}

func (f *reviewMemoryFixture) review3Resume(t *testing.T, sessionID, requestID string, after int64) int64 {
	t.Helper()
	tail := review2TailUser(t, getSession(t, f.url, sessionID).Messages)
	if code := doJSON(t, http.MethodPost, f.url+"/v1/sessions/"+sessionID+"/runs", protocol.PromptRequest{ConversationID: sessionID, ClientRequestID: requestID, ResumeMessageID: tail.ID, Prompt: tail.Content[0].Text}, nil); code != http.StatusAccepted {
		t.Fatalf("resume status %d", code)
	}
	events := waitForRun(t, f.url, sessionID, after)
	return events[len(events)-1].Seq
}

func (f *reviewMemoryFixture) review3LastRequestText() string {
	f.client.scriptedClient.mu.Lock()
	defer f.client.scriptedClient.mu.Unlock()
	last := f.client.scriptedClient.requests[len(f.client.scriptedClient.requests)-1]
	var whole strings.Builder
	for _, message := range last {
		whole.WriteString(message.TextContent())
		whole.WriteString("\n")
	}
	return whole.String()
}

// 关闭期间发出一条消息并停止，重新打开后继续（resume_message_id）。这是重新打开后的一轮：
// 工具里已经有 K-brain 记忆工具，但 resume 路径丢弃了本轮计算出的记忆快照，尾消息上只留着
// 关闭时的撤回，于是发给对话模型的请求说 "Memory is turned off"，且没有任何记忆条目。
func TestReview3MemorySwitchResumeAfterReenableStillSaysMemoryOff(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	f := newReviewMemoryFixture(t, t.TempDir(), reviewFixtureConfig())
	sess := createTestSession(t, f.url)

	seq := f.turn(t, sess.ID, "on-1", "hello there friend", 0)
	if !strings.Contains(f.review3LastRequestText(), "prefers-go") {
		t.Fatalf("baseline: memory not injected while on:\n%s", f.review3LastRequestText())
	}
	f.setEnabled(t, false)
	seq = f.review3CancelledTurn(t, sess.ID, "off-1", "please look at the build script", seq)
	if text := f.review3LastRequestText(); !strings.Contains(text, "Memory is turned off") {
		t.Fatalf("precondition: off turn must retract memory:\n%s", text)
	}

	f.setEnabled(t, true)
	f.review3Resume(t, sess.ID, "on-resume", seq)
	tools, _ := f.client.lastRequest(t)
	text := f.review3LastRequestText()
	last := strings.LastIndex(text, "Memory is turned off")
	restored := strings.LastIndex(text, "prefers-go")
	if last > restored {
		t.Fatalf("memory is back on (tools=%v) but the resumed request still ends on the off notice and carries no memory after it:\n%s", tools, text)
	}
}

// 关闭时同一条消息停止两次、恢复两次：markResumedMessage 对已带标记的尾消息直接返回，
// 撤回只出现一次。（验证用例，预期通过。）
func TestReview3MemorySwitchRepeatedResumeWhileOffRetractsOnce(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	f := newReviewMemoryFixture(t, t.TempDir(), reviewFixtureConfig())
	sess := createTestSession(t, f.url)

	seq := f.review3CancelledTurn(t, sess.ID, "on-1", "first message while on", 0)
	f.setEnabled(t, false)
	seq = f.review3CancelledTurnResume(t, sess.ID, "off-resume-1", seq)
	f.review3Resume(t, sess.ID, "off-resume-2", seq)
	text := f.review3LastRequestText()
	if n := strings.Count(text, "Memory is turned off"); n != 1 {
		t.Fatalf("want exactly one retraction after two resumes while off, got %d:\n%s", n, text)
	}
	if tools, _ := f.client.lastRequest(t); slices.Contains(tools, "memory_manager") {
		t.Fatalf("memory tool offered while off: %v", tools)
	}
}

// review3CancelledTurnResume resumes the tail and cancels that resume as well.
func (f *reviewMemoryFixture) review3CancelledTurnResume(t *testing.T, sessionID, requestID string, after int64) int64 {
	t.Helper()
	f.client.mu.Lock()
	before := len(f.client.tools)
	f.client.mu.Unlock()
	f.client.scriptedClient.mu.Lock()
	f.client.scriptedClient.wait = true
	f.client.scriptedClient.mu.Unlock()
	tail := review2TailUser(t, getSession(t, f.url, sessionID).Messages)
	var accepted protocol.RunAccepted
	if code := doJSON(t, http.MethodPost, f.url+"/v1/sessions/"+sessionID+"/runs", protocol.PromptRequest{ConversationID: sessionID, ClientRequestID: requestID, ResumeMessageID: tail.ID, Prompt: tail.Content[0].Text}, &accepted); code != http.StatusAccepted {
		t.Fatalf("resume status %d", code)
	}
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
	if code := doJSON(t, http.MethodPost, f.url+"/v1/sessions/"+sessionID+"/runs/"+accepted.RunID+"/cancel", map[string]any{}, nil); code >= 300 {
		t.Fatalf("cancel status %d", code)
	}
	events := waitForRun(t, f.url, sessionID, after)
	f.client.scriptedClient.mu.Lock()
	f.client.scriptedClient.wait = false
	f.client.scriptedClient.mu.Unlock()
	return events[len(events)-1].Seq
}

// /branch 复制源会话关闭期间的消息（含 memory-off 快照）。分支会话在开启状态下跑一轮，
// 提取器不应看到源会话关闭期间的原话。（验证用例，预期通过。）
func TestReview3MemorySwitchBranchKeepsOffMarks(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	f := newReviewMemoryFixture(t, t.TempDir(), reviewFixtureConfig())
	sess := createTestSession(t, f.url)
	const secret = "OFFSECRET-BRANCH-6620 I always deploy on Fridays"

	f.setEnabled(t, false)
	f.turn(t, sess.ID, "off-1", secret, 0)
	f.setEnabled(t, true)

	current := getSession(t, f.url, sess.ID)
	anchor := review2TailUserBefore(t, current.Messages)
	var branched protocol.Session
	if code := doJSON(t, http.MethodPost, f.url+"/v1/sessions/"+sess.ID+"/branch", protocol.BranchSessionRequest{
		ExpectedRevision: current.Revision,
		MessageRef:       protocol.HistoryMessageRef{MessageID: anchor.ID, Role: protocol.RoleUser},
	}, &branched); code != http.StatusCreated {
		t.Fatalf("branch status %d", code)
	}
	if branched.ID == "" || branched.ID == sess.ID {
		t.Fatalf("fixture: branch did not create a new session: %+v", branched)
	}
	f.turn(t, branched.ID, "branch-on-1", "I prefer short commit messages", 0)
	review2AssertNoLeak(t, f.review2WaitExtraction(t), "OFFSECRET-BRANCH-6620")
}

func review2TailUserBefore(t *testing.T, messages []protocol.Message) protocol.Message {
	t.Helper()
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == protocol.RoleUser {
			return messages[i]
		}
	}
	t.Fatal("fixture: no user message")
	return protocol.Message{}
}
