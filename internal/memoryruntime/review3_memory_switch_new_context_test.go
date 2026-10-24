package memoryruntime

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// review3NewContextClient: the first request (an off turn) is stopped by the user; the
// resumed request calls the real new_context tool; everything after answers plainly.
type review3NewContextClient struct {
	fixtureClient
	mu     sync.Mutex
	n      int
	cancel context.CancelFunc
}

func (c *review3NewContextClient) Clone() ai.Client { return c }

func (c *review3NewContextClient) Stream(_ context.Context, _ ai.Request, _ func(string), _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	c.n++
	n := c.n
	cancel := c.cancel
	c.mu.Unlock()
	switch n {
	case 1:
		cancel()
		return ai.Message{}, ai.Usage{}, context.Canceled
	case 2:
		call := ai.ToolCall{ID: "call-nc", Type: "function"}
		call.Function.Name = "new_context"
		call.Function.Arguments = `{}`
		return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{call}}, ai.Usage{}, nil
	}
	return ai.Message{Role: "assistant", Content: "done"}, ai.Usage{}, nil
}

// 关闭期间发的消息被停止，重新打开后继续；继续的这一轮模型调用 new_context。
// resetContextMessages 把尾部 user 原话拷进 "Current task:" 通知，通知在开启状态下生成，
// contextSnapshots 不带 memory-off 标记。下一轮开启的提取把这条通知（含关闭期间原话）
// 作为 earlier-user-messages 发给提取模型。
func TestReview3NewContextNoticeCarriesOffMessageIntoExtraction(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	extraction := &fixtureClient{}
	rt, _ := testRuntime(t, extraction)
	t.Cleanup(func() { _ = rt.Close() })
	chat := &review3NewContextClient{}
	ag := agent.New(chat, "m", 1000, "sys")
	ag.SetMemoryRuntime(rt)
	const secret = "OFFSECRET-NEWCTX-7781 I always deploy on Fridays"

	ag.SetMemoryEnabled(false)
	ctx, cancel := context.WithCancel(context.Background())
	chat.mu.Lock()
	chat.cancel = cancel
	chat.mu.Unlock()
	if _, err := ag.Turn(ctx, secret, agent.Events{}); err == nil {
		t.Fatal("fixture: off turn was not stopped")
	}
	msgs := ag.MessagesSnapshot()
	tail := msgs[len(msgs)-1]
	if tail.Role != "user" || tail.TextContent() != secret {
		t.Fatalf("fixture: tail is not the stopped off message: %+v", tail)
	}

	ag.SetMemoryEnabled(true)
	if _, err := ag.ContinueUser(context.Background(), tail.ID, agent.Events{}); err != nil {
		t.Fatal(err)
	}
	carried := false
	for _, m := range ag.MessagesSnapshot() {
		if m.Role == "user" && strings.Contains(m.TextContent(), "Current task:") && strings.Contains(m.TextContent(), "OFFSECRET-NEWCTX-7781") {
			carried = true
		}
	}
	if !carried {
		t.Fatal("fixture: new_context notice did not carry the resumed message")
	}
	time.Sleep(50 * time.Millisecond)
	extraction.mu.Lock()
	resumedCalls := extraction.calls
	extraction.mu.Unlock()
	if resumedCalls != 0 {
		t.Fatalf("precondition: the resumed off message itself must not be extracted (calls=%d)", resumedCalls)
	}

	if _, err := ag.Turn(context.Background(), "I prefer pnpm from now on", agent.Events{}); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, func() bool { extraction.mu.Lock(); defer extraction.mu.Unlock(); return extraction.calls > 0 }) {
		t.Fatal("extraction never ran after re-enabling")
	}
	extraction.mu.Lock()
	defer extraction.mu.Unlock()
	for _, request := range extraction.requests {
		if prompt := request.Messages[1].TextContent(); strings.Contains(prompt, "OFFSECRET-NEWCTX-7781") {
			t.Fatalf("a message written while memory was off reached the extractor through the new_context notice:\n%s", prompt)
		}
	}
}
