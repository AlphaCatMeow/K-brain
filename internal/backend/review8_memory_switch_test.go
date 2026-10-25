package backend

// Eighth independent review of the memory master switch (through 6773ffa). Reuses
// newReviewMemoryFixture and the review3 helpers (real backend, real memoryruntime);
// LIVEAGENT_HOME is isolated.

import (
	"strings"
	"testing"
)

const review8OnNotice = "Memory is turned on again"

// 桌面端主路径：会话开启时已注入过记忆索引（prefers-go），关闭一轮（说明落在 runtime-memory
// 来源上），再打开。开启那一轮重新注入索引，6773ffa 新增的“记忆已重新打开”循环因为该来源本轮
// 已有快照而跳过；模型看到的最后一条行为指令仍是“不要以任何方式保存记忆，被要求记住时告诉用户
// 记忆已关闭”，而 memory_manager 工具已经回来了。
func TestReview8ReenableAfterMemoryCarriedSessionLiftsOffInstruction(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	f := newReviewMemoryFixture(t, t.TempDir(), reviewFixtureConfig())
	sess := createTestSession(t, f.url)

	seq := f.turn(t, sess.ID, "on-1", "hello there friend", 0)
	if !strings.Contains(f.review3LastRequestText(), "prefers-go") {
		t.Fatalf("fixture: first turn must carry the memory index:\n%s", f.review3LastRequestText())
	}
	f.setEnabled(t, false)
	seq = f.turn(t, sess.ID, "off-1", "a message while off", seq)
	if n := strings.Count(f.review3LastRequestText(), review7Notice); n != 1 {
		t.Fatalf("fixture: off turn must carry one off notice, got %d", n)
	}
	f.setEnabled(t, true)
	f.turn(t, sess.ID, "on-2", "back on, please remember I use tabs", seq)
	text := f.review3LastRequestText()
	if strings.LastIndex(text, "prefers-go") < strings.LastIndex(text, review7Notice) {
		t.Fatalf("fixture: re-enabled turn must re-inject the index after the notice:\n%s", text)
	}
	if tools, _ := f.client.lastRequest(t); !hasMemoryTool(tools) {
		t.Fatalf("fixture: re-enabled turn lost memory tools: %v", tools)
	}
	if n := strings.Count(text, review8OnNotice); n != 1 {
		t.Fatalf("re-enabled turn must lift the off instruction exactly once, got %d; request still says %q with memory tools back:\n%s", n, review7Notice, text)
	}
}

// 验证（预期通过）：从未注入过记忆的会话（说明只落在一个来源上），开启后恰好一次“重新打开”，
// 后续轮次不重复；saved/runtime 两个来源不会各发一次。
func TestReview8ReenableInFreshOffSessionLiftsOnce(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	f := newReviewMemoryFixture(t, t.TempDir(), reviewFixtureConfig())
	f.setEnabled(t, false)
	sess := createTestSession(t, f.url)
	seq := f.turn(t, sess.ID, "off-1", "first off message", 0)
	f.setEnabled(t, true)
	seq = f.turn(t, sess.ID, "on-1", "back on", seq)
	f.turn(t, sess.ID, "on-2", "still on", seq)
	if n := strings.Count(f.review3LastRequestText(), review8OnNotice); n > 1 {
		t.Fatalf("on notice sent %d times, want at most once:\n%s", n, f.review3LastRequestText())
	}
}
