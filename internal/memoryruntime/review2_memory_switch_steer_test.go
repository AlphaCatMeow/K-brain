package memoryruntime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
)

// 关闭期间回合内 Steer 进来的 user 消息（后台子代理报告、wait/workflow 通知；TUI 里是用户
// 直接输入的插话）在 agent.go 里按 {Role:"user", Content, Parts, SentAt} 追加，没有
// memory-off 快照，withoutMemoryOffTurns 过滤不到。重新打开后它作为 earlier-user-messages
// 进入提取请求。
func TestReview2SteerDuringMemoryOffReachesExtractorAfterReenable(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	extraction := &fixtureClient{}
	rt, _ := testRuntime(t, extraction)
	t.Cleanup(func() { _ = rt.Close() })
	report := "OFFSECRET-STEER-6610 the user said they always deploy on Fridays"
	chat := &backgroundSubagentClient{report: report, subDone: make(chan struct{})}
	ag := agent.New(chat, "m", 1000, "sys")
	ag.SetMemoryRuntime(rt)

	ag.SetMemoryEnabled(false)
	if _, err := ag.Turn(context.Background(), "OFF-TURN scan the repository", agent.Events{}); err != nil {
		t.Fatal(err)
	}
	steered := false
	for _, m := range ag.MessagesSnapshot() {
		if m.Role == "user" && strings.Contains(m.TextContent(), report) {
			steered = true
		}
	}
	if !steered {
		t.Fatal("fixture: subagent report was not steered into the off turn")
	}
	time.Sleep(100 * time.Millisecond)
	extraction.mu.Lock()
	offCalls := extraction.calls
	extraction.mu.Unlock()
	if offCalls != 0 {
		t.Fatalf("extractor ran while memory is off: %d", offCalls)
	}

	ag.SetMemoryEnabled(true)
	if _, err := ag.Turn(context.Background(), "I prefer pnpm from now on", agent.Events{}); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, func() bool { extraction.mu.Lock(); defer extraction.mu.Unlock(); return extraction.calls > 0 }) {
		t.Fatal("extraction never ran after re-enabling")
	}
	extraction.mu.Lock()
	defer extraction.mu.Unlock()
	for _, request := range extraction.requests {
		prompt := request.Messages[1].TextContent()
		if strings.Contains(prompt, "OFF-TURN") {
			t.Fatalf("fixture broken: off-turn user message itself leaked:\n%s", prompt)
		}
		if strings.Contains(prompt, "OFFSECRET-STEER-6610") {
			t.Fatalf("a user-role message steered in while memory was off reached the extractor after re-enabling:\n%s", prompt)
		}
	}
}
